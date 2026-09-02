# 游聚（X-Zone / youjuhang）协议地图

> 来源：项目 `docs/protocol_notes.md`、`docs/traffic_analysis.md`、`docs/zone_hang_fix.md`
> 与 2026-08-26 ~ 08-29 的 pcap / frida 实测。
> **本文档只记录实测确认过的内容。发现与本文冲突时，以实测为准并回来更新这里。**

## 一、通道总览

| 端口 | 协议 | 明文? | 用途 |
|---|---|---|---|
| 18000 | HTTP/2 | 是 | 登录 `POST /login/`（域名 `gamelogin3.gotvg.com`） |
| 180xx / 181xx（登录响应下发，动态） | HTTP/2 | 是 | 业务 `POST /game/` 与区/房间 `POST /zone/2/` **共用同一连接** |
| 18140 | 自定义二进制 | 否（加密） | 大厅长连接、实时推送。挂机程序不依赖 |
| 18180 | 自定义二进制 | 部分 | 大厅推送/心跳 |
| UDP | RakNet | — | 游戏内对战，与本程序无关 |
| 18080（gotvgmall.gotvg.cn） | HTTP | 是 | 商城 Web / CEF |

区/房间消息与业务消息共用同一 gate 地址与 HTTP/2 连接——这是最容易踩的坑之一：
同一条连接上既跑 `/game/`（256/512/515/1172/1174/1175/596）又跑 `/zone/2/`（1024~1092）。

## 二、HTTP/2 封帧

- 连接前言：`PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n`（24 字节）
- 帧头 9 字节：`[length:3][type:1][flags:1][stream_id:4]`
- 常见 type：`0x0 DATA` `0x1 HEADERS` `0x4 SETTINGS` `0x6 PING` `0x7 GOAWAY` `0x8 WINDOW_UPDATE`
- HEADERS 是标准 HPACK（Huffman），**未加密**。早期文档称"HEADERS 密文"是未实现 Huffman 解码导致的误判。
- 心跳：HTTP/2 PING，约 10 秒一次。

## 三、业务载荷编码

```
请求 DATA = base64url( [2B MsgID 小端] + protobuf )
响应 DATA = base64url( protobuf )                # /game/ 明文
          = base64url( [2B MsgID LE=264] + protobuf )   # /zone/2/，需剥掉前 2 字节
          = base64url( zlib(protobuf) )          # 大包可能压缩；也有 "+" 前缀 + zlib 的形态
```

关键编码坑：

- **必须用 base64url（`-` `_`），不能用标准 base64（+ /）**：服务端是 Go 的
  `base64.URLEncoding`，标准 base64 会出现 `illegal base64 data at input byte N`。
- base64 可能带填充也可能不带，解码时两种都要试。
- 响应**不含请求的消息 ID**，靠 HTTP/2 stream 号关联请求与响应。

## 四、消息 ID 表

### `/game/` 业务消息

| ID | 消息 | 说明 |
|---|---|---|
| 256 | LsLoginCMsg | 登录请求 |
| 258 | LsLoginSMsg | 登录响应 |
| **512** | **EnterLobbyCMsg** | **进大厅。房间消息的前置必需步骤，见下** |
| 515 | RefreshInfoCMsg | 保活/刷新（action=5） |
| 549 / 550 | HeartBeatCMsg / SMsg | 心跳 |
| 596 | TeamContributeCMsg | 战队。f3=15 捐献（f9={f1=amount}）；f3=3 战队详情（**响应结构见下**） |
| 1172 | MissionListCMsg | 任务列表 |

#### 596/f3=3 战队详情响应结构（2026-08-31 三账号对照实测）

响应为 `[+ 前缀] base64url(zlib(pb))`，由 `gate.raw()` 统一解码。解码后：

```
顶层: f1=ret(1=成功)  f6=<战队数据块>
```

f6 子字段（**关键：有无 f1 是"是否加入战队"的唯一可靠判据**）：

| 字段 | 有战队（chouyoku / wingser） | 无战队（youjugua） |
|---|---|---|
| f1 | **战队 ID = 10104** | **不存在（0）** |
| f2 | 战队公告（12 KB） | 不存在 |
| f3 | 成员列表（42 KB） | 可加入战队列表（42 KB） |
| f4 | **自身 UID**（6017780 / 5571561） | **自身 UID**（9068537） |
| f5 | 贡献度余额（6250 / 3250） | 不存在（0） |

**踩坑（曾导致严重误判）**：`f6.f4` 恒为**当前账号自身的 UID**（与登录 uid 一致），
`f6.f4 != 0` 恒为真，**不能**用于判定"已加入战队"，否则所有账号（含从未加入战队的
新注册账号）都会被误判为有战队。正确判据是 `f6.f1 != 0`。

证据：`captures/team_info_youjugua_20260831_00001_20260831171415.pcapng`
+ 探针 `cmd/probe_teaminfo`（已删除，逻辑见 `biz/contribute.go` 的 `queryTeamInfo`）。
| 1174 | MissionSubmitCMsg | 领奖。f3=missionIdList（bytes，varint 编码）f5=dailyOnline{day} |
| 1175 | PlayerCheckInCMsg | 签到（checkInType=2） |

### `/zone/2/` 区与房间消息

| ID | 消息 | 说明 |
|---|---|---|
| 516 | QueryZoneInfoCMsg | **进区前先查区信息**，建立会话 |
| 517 | EnterZoneCMsg | **真正进区**。响应 f3=对局服务器、f4=zoneId、**f9=区配置 JSON** |
| 1024 | QueryZonePlayerListCMsg | 区玩家列表（明文 protobuf） |
| 1025 | QueryZoneRoomListCMsg | **区房间列表（明文 protobuf，可解析）** |
| 1026 | EnterRoomCMsg | 进房/建房。f3=**roomId**；建房另加 f4=1(isCreate) f5=RoomCfgInfo f8=1 |
| 1027 | LeaveRoomCMsg | 离房 |
| 1028 | RoomHeartBeatCMsg | 房间心跳，约 1s。**仅玩家/房主** |
| 1032 | GetRoomSnapShotReplayCMsg | 快照轮询，约 1.6s。观战身份靠它保活 |
| 1033 | ChangeRoomSlotCMsg | 上位/换座，f3=slot（实测=1） |
| 1042 | RoomOnlineReportCMsg | **在线时长上报**，房主约 1s，f3={f1=3,f2=tick 递增} |
| 1080 | PresentGift2PlayerCMsg | 进房后约 0.5s 自动送礼 |
| 1092 | RoomStatusCMsg | 状态轮询，约 1.6s |

> 历史误判提醒：早期表格把 1024/1025 记成"ZoneInfo/ZoneConnect，响应 58105 强加密不可解析"，
> 把 1026 的 f3 记成 zoneId。两者**都是错的**，已实测更正。

## 五、关键数据结构

### 登录

```
LsLoginCMsg: f2=account  f3=MD5(密码).upper()(32hex, 无盐)  f4=mac  f6=raknetId(随机uint64)
            注意：f1(gameVersion)/f5(platform) 不发送
LsLoginSMsg: f1=ret  f3=服务器时间戳  f4=token(32hex)  f5={f1="ip:port"}=gate 地址
```

### 进区 517 响应

```
f1 = ret
f3 = "45.120.103.100:61001"   对局服务器
f4 = zoneId
f9 = 区配置 JSON: {"Name":"自由区","Id":1,"Limit":200,"MaxRoomNum":500,"Slot":2,
                   "VersionList":[{"Name":"fc_8bit","Title":"演奏起来","Id":1,
                                   "Rom1Md5":"dc06babfb280c32ae895a020c86b69fb"}, ...]}
f13/f14 = 附加字段（12B 二进制 / 10）
```

区 ID：实测 1=自由区（含 fc_8bit），曾长期用 2 导致 `ZONE_NOT_FOUND 3 1582 2`。

### 1025 房间列表响应（明文，重点）

```
f1 = ret(1=成功)
f3 = RoomInfo（重复，实测 187 条，房间号 1..187）
     f1 = roomId
     f2 = RoomCfgInfo { f1=房间名, f5=versionId, f12=obStatus, f23=创建时间戳 }
     f4 = { f1=hostUid, f3=1, f4=roomId }
     f5 = { f1=uid, f2=slot, f4=roomId }（重复）
     f6 = { ... }（重复，座位/版本信息）
     f7 = hostUid        ← 0 表示该号空闲，可直接建房
     f12 = { f1=hostUid }
f4 = 分页/统计
```

### 1026 进房/建房响应

```
f1 = ret
f3 = slot（255=观战；建房成功时无此字段）
f4 = RoomInfo { f1=roomId, f2=cfg, f3=?, f4=玩家列表?, f5=玩家列表,
                f7=房主uid, f8/f9/f10/f11=状态位, f12={f1=hostUid}, f14, f15, f16 }
```

**建房成功判定必须用 `f4.f7 == uid`（IsHost），不能靠 f5 玩家列表——
实测建房成功的响应里根本没有 f5。** 这是本项目踩过的最隐蔽的一个坑。

## 六、挂机消息时序

建房当房主：

```
516 → 517 → 1025(查列表) → 1026(建房,f4=1) → [1028 + 1042 每1s；1092 每1.6s] × N → 1027
```

进他人房间观战：

```
516 → 517 → 1025 → 1026(进房) → [1032 + 1092 每1.6s] × N（无 1028）→ 1033(上位) → 转房主时序
```

登录到能进房还必须先行一步：

```
256(登录) → 512(进大厅，激活 token) → 516 → 517 → ...
```

## 七、任务相关

- `MissionListCMsg(1172)` 响应 f3 为任务条目重复字段，每条：
  `f1=任务ID` `f2=目标/进度秒数` `f3=达成标记(1达成,0未达成)` `f5=领取时间戳(0=未领)`
- 在线时长档位实测：2150=600s、2151=1800s、2152=3600s、2153=7200s、2000=7200s
  **任务 ID 随账号/活动变化，必须动态获取，不能硬编码。**
- 提交领奖 `1174`：`ret=1` 成功；`-127 MISSION_ALREADY_FINISH`；`-128 MISSION_NOT_ACCOMPLISH`
