# 游聚 X-Zone 协议笔记（持续更新）

> 来源：`D:\Game\游聚平台\1\bin\X-Zone.exe` 静态分析 + frida 动态抓包（h5_xzone.log）+ pcap 分析

## 通信架构（已实测确认）

| 通道 | 技术 | 用途 |
|---|---|---|
| HTTP/2 登录 | `POST http://gamelogin3.gotvg.com:18000/login/` | 登录，响应下发 uid/token/网关地址 |
| HTTP/2 网关 | `POST http://<服务器下发 httpAddr>/game/`（如 `153.99.234.163:18061`） | 业务请求（任务/签到/刷新/上报） |
| TCP 长连接 | 端口 18140（服务器下发） | 大厅心跳、实时消息 |
| UDP | RakNet | 游戏内对战 |
| Web (CEF) | `gotvg_web_cef.exe` | 商城 `gotvgmall.gotvg.cn`、排行榜 |

**关键结论**：HTTP/2 连接为标准 HTTP/2（客户端 UA `Go-http-client/2.0`，Go 实现），
HEADERS 为**标准 HPACK（Huffman 编码）**，无加密。
之前"HEADERS 密文"结论是未实现 Huffman 解码导致的误判。

## 封帧格式（HTTP/2 DATA body）

- **请求 body** = `base64( [消息ID 2字节 LE] + [protobuf] )`
  - 例：`00 01` = LsLoginCMsg(256)，`96 04` = MissionSubmitCMsg(1174)，`94 04` = MissionListCMsg(1172)，`03 02` = RefreshInfoCMsg(515)
- **响应 body** = `base64( [protobuf] )`（**无消息 ID 前缀**，靠 H2 stream 关联）
  - base64 为标准或 URL-safe（含 `-`/`_`），可能分片传输
- 响应 HEADERS：`content-type: text/plain; charset=utf-8`，`content-length` = base64 文本长度

## 已确认的消息 ID（2 字节 LE 前缀）

| ID | 消息 | 说明 |
|---|---|---|
| 256 | LsLoginCMsg | LS 登录请求 |
| 258 | LsLoginSMsg | LS 登录响应 ret/uid/token/game |
| 512 | EnterLobbyCMsg | 进大厅 uid/gameToken/gameVersion |
| 515 | RefreshInfoCMsg | 刷新信息 uid/gameToken/action |
| 549 | HeartBeatCMsg | 心跳 |
| 550 | HeartBeatSMsg | 心跳响应 |
| 1172 | MissionListCMsg | 任务列表 uid/gameToken/missionTab |
| 1174 | MissionSubmitCMsg | 任务提交 uid/gameToken/missionIdList/action/dailyOnline |
| 1175 | PlayerCheckInCMsg | 每日签到 uid/gameToken/checkInType |
| 2304 | GateLoginCMsg | 网关登录（旧 18141 协议用） |
| 1024 | ZoneInfoCMsg | 区列表（发 /zone/2/，f1=uid f2=token，响应 f9=分区 JSON） |
| 1025 | ZoneConnectCMsg | 区连接确认（发 /zone/2/，f1=uid f2=token） |
| 1026 | CreateRoomCMsg | 进房（发 /zone/2/，f3=zoneId f8=1 进入区默认房间） |
| 1027 | LeaveRoomCMsg | 离房（发 /zone/2/，f1=uid f2=token） |
| 1028 | RoomHeartBeatCMsg | 房间心跳（约每秒 1 次） |
| 1092 | RoomStatusCMsg | 房间状态轮询 GetRoomSramMemCard（约每 1.6 秒 1 次） |

## 关键消息字段（proto/all.proto）

```proto
message LsLoginCMsg { uint32 gameVersion=1; string account=2; string password=3; string mac=4; int32 platform=5; uint64 raknetId=6; }
message LsLoginSMsg { Proto.Err ret=1; string errStr=2; uint32 uid=3; string token=4; Proto.ServerInfo game=5; int32 line=6; }
message ServerInfo { string httpAddr=1; int32 line=2; string tcpAddr=3; }
message RefreshInfoCMsg { uint32 uid=1; string gameToken=2; uint32 action=3; }
message MissionListCMsg { uint32 uid=1; string gameToken=2; uint32 missionTab=3; }
message MissionSubmitCMsg { uint32 uid=1; string gameToken=2; bytes missionIdList=3; MissionSubmitOpType action=4; MissionSubmitCMsg_DailyOnline dailyOnline=5; }
message MissionSubmitCMsg_DailyOnline { int32 day=1; }
message PlayerCheckInCMsg { uint32 uid=1; string gameToken=2; uint32 checkInType=3; }
```

## 登录流程（实测，00009 pcap + frida）

1. **HTTP/2 连接** `gamelogin3.gotvg.com:18000`
2. `POST /login/`，HEADERS：`content-type: application/x-www-form-urlencoded`, `user-agent: Go-http-client/2.0`
3. DATA = `base64([00 01][LsLoginCMsg])`
   - LsLoginCMsg：field1 gameVersion, field2 account("chouyoku"), field3 password(32hex = MD5), field4 mac("54-05-DB-91-34-AB"), field5 platform, field6 raknetId(随机 uint64)
4. 响应 = `LsLoginSMsg{ret=1, uid=49118644, token="e4b2360f66e92eaaa952724d0498e314", game=ServerInfo{httpAddr:"153.99.234.163:18061", line:1}}`

## 网关业务流程（实测，18061）

连接 `153.99.234.163:18061`，`POST /game/`，DATA=base64([ID][protobuf])：

1. **MissionListCMsg(1172)**：uid+token → 响应 MissionListSMsg（任务列表，field3 多条 mission）
2. **RefreshInfoCMsg(515, action=5)**：uid+token+action=5 → RefreshInfoSMsg（ret/change）
3. **MissionSubmitCMsg(1174)**：uid+token+missionIdList=[4328]+dailyOnline{day=26} → 领"在线1小时"奖励
4. 每 ~5 分钟新建 H2 连接，发 RefreshInfoCMsg(515)（保持在线/刷新时长）
5. H2 PING 心跳（连接存活）

**在线奖励 missionId=4328**（MissionSubmitCMsg.missionIdList=[4328]，dailyOnline.day=当日日期）。

> 注：4328 为旧账号(chouyoku)实测；新账号(5571561)任务列表中在线档位为 2150/2151/2153/2000。
> 任务 ID 随账号/活动变化，**挂机程序必须通过 MissionListCMsg 响应动态获取**，不能硬编码。

### 2026-08-26 协议复现验证（tools/verify_gate.py）

用真实 uid/token 通过独立 HTTP/2 连接（h2 库）直接请求网关成功：

1. `MissionListCMsg(1172)` → 200，返回 MissionListSMsg（任务列表 556B），与客户端抓包一致
2. `RefreshInfoCMsg(515, action=5)` → 200，ret=1，返回 ChangeInfo 时间戳更新
3. 请求 body 编码：**标准 base64 带 padding**（`base64([2B MsgID LE][protobuf])`）；响应 body 同为 base64

结论：**协议 100% 可复现，token 直接可用，无需依赖客户端**。挂机程序可仅凭 `uid + token` 完成业务请求。

## 签到与每日在线（2026-08-26 实测，UID 5571561）

- **签到请求** = `PlayerCheckInCMsg(1175)`，实测 body：`97 04 [08 uid][12 20 token][18 02]`
  - `checkInType=2`（实测）。枚举字符串：`DAILY_SIGN_IN` / `MONTH_SIGN_DAY_NUM` / `GUILD_SIGN_IN`
  - 打开签到弹窗**不触发** PlayerCheckInCMsg（h6 实测，弹窗状态不走 H2 网关，可能 H5/本地缓存）
- **在线时长任务**：服务器自动累计在线时长；达到档位后任务状态变化，客户端发
  `MissionSubmitCMsg(1174)` 领取奖励。实测 body：
  `96 04 [08 uid][12 20 token][1a 02 e6 10][2a 02 08 1a]`
  - field3 = missionIdList，**bytes 类型**，内容为任务ID的 varint 编码（如 `e6 10`=2150，`e7 10`=2151）
  - field5 = dailyOnline{day=当日}（nested message）
- **每日在线任务 ID（MissionListSMsg 实测）**：

  | ID | 目标 | 说明 |
  |---|---|---|
  | 2150 | 600s | 在线10分钟 |
  | 2151 | 1800s | 在线30分钟 |
  | 2153 | 7200s | 在线2小时 |
  | 2000 | 7200s | 每日在线（2小时） |
  | 2001-2008 | - | 日常任务（1005/1002 等另属活动） |

- **任务状态（2026-08-26 Go 挂机程序真机实测确认）**：
  - Mission{f1=id, **f2=目标秒数**, **f3=达成标记(1=达成,0=未达成)**, **f5=完成/领取时间戳(0=未完成)}
  - 例：2150{f2=600,f3=1,f5=已领时间戳} → MISSION_ALREADY_FINISH；2153{f2=7200,f3=1,f5=0} → **可领取**（MISSION 发放成功）
  - 提交领取后任务响应：ret=1 成功；ret=-127(errStr="MISSION_ALREADY_FINISH <id>") 已完成；ret=-128("MISSION_NOT_ACCOMPLISH <id>") 未达成
- **MissionSubmitSMsg 响应**：f1=ret(1 成功)，f100=ChangeInfo（含任务更新与经验奖励，如 `1a 03 45 58 50` = "EXP"）
- `CheckXzoneSignReq/AddXzoneSignReq`（`TVG_SRAM_XZONE_SIGN_*`）属 SRAM 存档签名（房间存档上传校验），**与每日签到无关**，含 `md5_encrypt` 字段（算法仅存档校验需要）

## TCP 大厅（18140）

- 客户端帧：`[4B len LE][2B msgID LE][payload]`（如 `02 00 00 00 25 02 00` = len2 + HeartBeatCMsg(549)）
- 心跳间隔待确认（约 30-60s）

## 密码哈希算法（2026-08-26 实测确认，h7_xzone.log）

- **field3 = MD5(密码明文) 的 hex 大写**（无盐、无双重哈希）
- 实测：账号 `wingzer`，密码 `test1234` → `MD5("test1234").upper() = 16D7A4FCA7442DDA3AD93C9A726597E4` = 抓包 field3 ✓
- LsLoginCMsg 实测完整字段：`[00 01]` + f2=account("wingzer") + f3=MD5(密码) + f4=mac("54-05-DB-91-34-AB0")
- 登录请求为客户端自动登录（attach 后 ~2s 发出），HTTP/2 POST /login/，body=base64([00 01][LsLoginCMsg])

## 登录闭环完整确认（2026-08-26，pcap payloads 解码 + h7 实测）

### LsLoginCMsg（登录请求，实测发送的字段）

```
body = base64( 00 01 | protobuf{
  field2: account(明文, 如 "chouyoku"/"wingzer"),
  field3: MD5(密码).upper()  (32 hex, 无盐),
  field4: mac "54-05-DB-91-34-AB" (注册表 MAC, 标准格式),
  field6: raknetId(随机 uint64)
})
POST http://gamelogin3.gotvg.com:18000/login/  (HTTP/2 明文, Go-http-client/2.0)
```
- **注意：f1(gameVersion)/f5(platform) 在请求中不发送**（proto 定义有但留空）
- 客户端自动登录也是同样格式（h7 实测 wingzer/test1234 → field3=16D7A4FCA7442DDA3AD93C9A726597E4）

### LsLoginSMsg（登录响应）

```
f1 = 1 (成功)
f3 = 6017780 (服务器时间戳)
f4 = token (32 hex 字符串) ← 关键
f5 = { f1="153.99.234.163:18141" } ← Gate 地址 (动态下发)
```

### Gate 连接认证（连 f5 的 ip:port 后）

- 认证消息: `a5 ef 02` + `12 20 <token 32字节>`（含 UID 的消息在 5284 样本: `a5 ef 02 12 20 token`）
- 后续业务走二进制封帧 `[00 02]` + 消息（详见封帧表）

### 登录闭环状态

- [x] HTTP 登录请求格式（H2 + base64 + [00 01] + LsLoginCMsg）
- [x] 密码哈希 = MD5(密码).upper()（wingzer/test1234 实测匹配）
- [x] 登录响应解析（f4=token, f5=gate 地址）
- [x] Gate 认证消息（UID + token）
- [x] 心跳（10s 节拍 + 抖动）
- [x] 签到请求 PlayerCheckInCMsg(1175) checkInType=2（h5 实测）
- [x] 每日在线 MissionSubmitCMsg_DailyOnline（h6 实测）

## 脚本实测登录闭环（2026-08-26，tools/verify_login_flow.py）

`python tools/verify_login_flow.py chouyoku test1234 54-05-DB-91-34-AB` 全流程成功：

```
1. HTTP/2 登录 gamelogin3.gotvg.com:18000 POST /login/
   body = base64url([00 01][LsLoginCMsg{account, MD5(密码).upper(), mac, 随机uint64}])
   → ret=1, uid=6017780, token="d807176f0c4e74805b4e7e1c728fe89c", gate="61.147.93.135:18071"
2. HTTP/2 Gate POST /game/ body=base64url([2B MsgID LE][proto{uid, token}])
   MissionListCMsg(1172) → 200, MissionListSMsg 任务列表(577B, f1=1 成功)
```

### 关键编码坑（已踩过）

- **body 必须用 base64url（- _），不能用标准 base64（+ /）**：服务器用 Go
  `base64.URLEncoding` 解码；随机 raknetId 的 varint 常含字节使标准 base64 产生 `+`/`/`，
  导致 "illegal base64 data at input byte N"
- 登录请求**不发送 f1(gameVersion)/f5(platform)**（proto 有定义但留空）
- `wingzer` 为无效账号（服务器返回 ret=-2 `ACCOUNT_NOT_FOUND`），chouyoku 有效

## 签到响应语义（2026-08-26 Go 程序真机实测）

- 请求：PlayerCheckInCMsg(1175) body `[08 uid][12 20 token][18 02]`（checkInType=2）
- 响应：当日已签到（或点数不足）返回 **ret=-23, errStr="point not meet"**——服务器理解请求但拒绝
- 成功路径待明日新的一天未签到时实测（当前所有账号今日已签）

## 战队任务与挂机时长奖励（2026-08-27 实测，team_task pcap）

### 任务查询（战队→战队任务）
- 战队任务查询 = `MissionListCMsg(1172)`（发 `/game/`，f1=uid f2=token）→ 响应 `MissionListSMsg(264)` **明文 protobuf，完全可解析**。
- 响应 f3 为任务条目列表，每条：`f1=任务ID`、`f2=目标/进度秒数`、`f3=达成标记(1=达成)`、`f5=领取时间戳(0=未领)`。
- 注：`58105` 大响应（40KB）为强加密数据（熵≈7.99 bit/byte），与任务查询无关，不触碰。

### 实测在线时长任务档位（抓包时用户进度 2h+）
| 任务ID | 目标秒数 | 说明 | 抓包时状态 |
|---|---|---|---|
| 2150 | 600 (10分钟) | 在线时长 | 已领取 |
| 2151 | 1800 (30分钟) | 在线时长 | 已领取 |
| 2152 | 3600 (1小时) | 在线时长 | 已领取 |
| 2000 | 7200 (2小时) | 每日在线 | 已领取 |
| 2153 | 7200 (2小时) | 在线时长 | 达成未领 |
- **3 小时档（10800s）在 2h+ 进度时尚未出现在任务列表**（推测 ID=2154，待挂满 3 小时后复查确认）。
- 挂机自动退出判定（`server/internal/biz/hangGoalDone`）：任务 ID 映射==10800 **或** f2==10800，且 `f3=1`（达成）且 `f5!=0`（已领取）。

### 战队贡献捐献（2026-08-27 实测，team_contribute pcap）
- 消息 = `TeamContributeCMsg(596)`，发 `/game/`，请求 body=base64url(`[02 05]`+protobuf)，响应**无 msgid 前缀**直接 protobuf。
- **捐献请求**（实测 500/1000 两次）：
  ```
  f1=uid  f2=token  f3=15(0x0f 捐献动作)  f9={ f1=amount }  (bytes 子消息)
  例捐500: 08 f4a5ef02 12 20 <token> 18 0f 4a 03 08 f403
  例捐1000: 08 f4a5ef02 12 20 <token> 18 0f 4a 03 08 e807
  ```
- **捐献响应**：`f1=ret(1=成功)` `f8={ f1=捐前余额 f2=捐后余额 f3=? f4=? }`
  - 捐500 → f8={29000, 28500, 5350, 5355}（f1=捐前、f2=捐后）
  - 再捐1000 → f8={28500, 27500, 5355, 5365}（与用户"剩27500余额"吻合）
- **f3=3（战队详情查询）**：同 msgid 596，响应 = `[0x2B('+') 前缀] base64url(zlib(pb))` 大包（解压后 ~54KB）。
  - 结构：`root{f1=ret f6={f4=uid f5=贡献度余额 f2=成员列表 f3=贡献记录}}`，余额字段 `root.f6.f5`（实测 29000，与捐献前余额一致）。
  - 网关 `gate.Client.raw` 已支持 `+` 前缀（截掉后再 base64url 解码，tryDecompress 解 zlib）。
- 注意：捐献消耗贡献度（余额 29000→27500），属消耗性操作。
- **自动捐献**：`biz/contribute.go`，每日目标 3000、余额 <3000 不捐、按账号持久化当日已捐
  （`<配置目录>/contribute_state.json`），重复登录/重启不重复捐献。

### 挂机区（"8bit音乐(挂机区)"）
- 用户选择进入的挂机区 **zoneID=2**（`POST /zone/2/` 即此区）；进房 `CreateRoomCMsg(1026) f3=2 f8=1`。
- 与旧抓包的自由区（zoneID=1）不同；`room_hang_zone` 配置已改为 2。
- 挂机监督（`hangSupervisor`）：每 5 分钟查一次任务，3 小时奖励领取完成即退出挂机；超 3 小时未达成则每 10 分钟复查。

## 分区/房间挂机协议（2026-08-27 实测，team pcap + h2_stream20/50 + hang_spectate pcap）

### 连接与路径
- 区/房间消息与业务消息**共用同一 gate 地址与 HTTP/2 连接**（实测 61.147.93.135:18151 与 18141 上同时有 `/game/` 与 `/zone/2/`）。
- 路径：区/房间消息 → `POST /zone/2/`，body 仍为 `base64url([2B MsgID LE][protobuf])`。
- **响应格式（实测 8-27，重要）**：
  - `/game/` 响应 = `base64url(明文 protobuf)`，**不含 MsgID 前缀**（如 1172 响应 head=`08 01 ...`=f1=ret=1），直接 proto.Parse 即可；
  - `/zone/2/` 响应 = `base64url(zlib 或明文)([2B MsgID LE=264][protobuf])`，**含 MsgID 前缀**，解析前需剥掉前 2 字节；
  - base64 可能为 url-safe 带填充或无填充，解码需两种都试；
  - ~~大响应（玩家/房间列表 1024/1025）msgid=58105，payload 为强加密数据，无法解析~~
    **2026-08-29 更正：此结论错误。** 请求体只带 `f1=uid f2=token`（无额外参数）时，
    1025 返回的就是明文 protobuf，实测 187 个房间可直接解析，详见下方「房间列表 1025」。
    该误判曾导致长期只能随机盲猜房间号。
- 建房/进房响应 = 264 消息体即 EnterRoomSMsg：`f1=ret`、`f3=slot(观战=255)`、`f4=RoomInfo{f1=roomId f2=cfg(房间名=f1) f4=玩家列表 f5=观战列表 f7=房主uid}`；
  **建房成功判定 = 本机出现在 f4 玩家列表（playerList）**（h2_stream50 建房响应无 f3；观战响应 f3=255 且本机在 obList）。
- 任务列表（1172）实测：达成任务 `f3=accomplish=1` 且 `f5=submitTime` 非零（时间戳，如 1787037180），服务器在达成/发放后设置；
  "战队界面达成任务无需手动领奖"= 服务器自动发奖模型，客户端提交 1174 领取返回 AlreadyFinish 即幂等。

### 消息 ID 纠正（覆盖上方表格旧记录）
| ID | 旧记录（误） | 实测真相（proto/all.proto 为准） |
|----|------------|-------------------------------|
| 1024 | ZoneInfoCMsg（区列表，响应 f9=区 JSON） | **QueryZonePlayerListCMsg**（区玩家列表），明文可解析 |
| 1025 | ZoneConnectCMsg | **QueryZoneRoomListCMsg**（区房间列表），**明文可解析**（2026-08-29 更正，非加密） |
| 1026 | CreateRoomCMsg | **EnterRoomCMsg**（f3=**roomId** 而非 zoneId！f4=isCreate f5=cfg） |
| 516 | - | QueryZoneInfoCMsg（进区前查询区信息，响应明文 f3 嵌套） |
| 517 | - | **EnterZoneCMsg**（真正进入分区！响应 f3=对局服务器 ip:port、f4=zoneId、**f9=区配置 JSON**） |
| 1080 | - | PresentGift2PlayerCMsg（进房后约 0.5s 自动发送，f6=房间内玩家 uid 列表，进房打招呼/送礼） |
| 1032 | - | GetRoomSnapShotReplayCMsg（观战时轮询快照，`f3=1 f6=seq`，无快照返回 SNAPSHOT_TOO_OLD） |
| 1033 | - | **ChangeRoomSlotCMsg**（观战上位/换座，`f1=uid f2=token f3=slot(实测上位=1)`；响应 264 空体=成功，随后恢复 1028 心跳） |

### 进房 EnterRoomCMsg(1026)（f3 是 roomId！）
- 实测进已有房间（hang_spectate pcap）：`f1=uid f2=token f3=roomId(46) f8=1`（45B）→ 作为**观战者**进入（响应 f3=slot=255）。
- 实测建房（h2_stream50 pcap）：`f1=uid f2=token f3=roomId(167) f4=1(isCreate) f5=房间配置 f8=1`（132B）
  - f5 房间配置：`f1=房间名"大家一起玩" f2=1 f3="1" f5=1 f10=1 f12=2(zoneId) f15=ROM_MD5 f18=637665792 f20="fc_8bit" f24=1 f8=1`
  - ROM_MD5（fc_8bit 演奏起来）= `dc06babfb280c32ae895a020c86b69fb`
- **旧记录"形态2 f3=zoneId"系误判**：`f3=2` 实际是 roomId=2（zoneId 与 roomId 数值巧合相同）。
- 响应（264 信封）：
  - `f3=slot`：**255=观战**（房间满/已有玩家）；建房成功无此字段。
  - `f4=RoomInfo`：`f1=roomId`、`f2={f1=房间名 f5=? f12=zoneId f23=时间戳}`、`f3=房主?`、
    `f4=repeated 玩家`（seat 1/2，`f1=uid f3=seat`）、`f5=repeated 观战者`（seat 255）、
    `f6=游戏状态`、`f7/f8/f14/f16` 等。

### 房间列表 QueryZoneRoomListCMsg(1025)（2026-08-29 实测更正）

请求体只有 `f1=uid f2=token`（不带任何额外参数），响应**明文 protobuf**：

```
f1 = ret(1=成功)
f3 = RoomInfo（重复，实测 187 条，房间号 1..187）
     f1 = roomId
     f2 = RoomCfgInfo { f1=房间名, f5=versionId, f12=obStatus, f23=创建时间戳 }
     f4 = { f1=hostUid, f3=1, f4=roomId }
     f5 = { f1=uid, f2=slot, f4=roomId }（重复）
     f6 = { ... }（重复）
     f7 = hostUid          ← 0 表示该号空闲，可直接建房；非 0 表示已有人
     f12 = { f1=hostUid }
f4 = 分页/统计信息
```

要点：

- **房间号必须取自本列表**。列表外（如 188）建房 → `-19 ROOM_NOT_FOUND <id>`；
  列表内已占用 → `-87 WRONG_ROOM_STATUS <id> RS_GAMING`。
- 用 `f7 == 0` 判断空闲号，建房当房主。
- 本响应在「未在房间内」时查询才完整；已进房时查询形态会变，故应在进房前拉取。

### 进大厅 EnterLobbyCMsg(512) 是房间消息的前置必需步骤（2026-08-29 实测）

对照实验（同账号、同房间号 43）：

```
跳过 512：256登录 → 516 → 517 → 1026 JOIN 43   → ret=-10 "WRONG_GAME_TOKEN"
发送 512：256登录 → 512 → 516 → 517 → 1026 JOIN 43 → ret=1 成功
```

**迷惑点**：`/game/` 侧消息（516/517、任务、战队、签到）不发送 512 也能正常响应，
只有 `/zone/2/` 的房间消息校验 token 激活状态。517 会成功，造成"token 没问题"的错觉。
因此 `session.Login` 在登录成功后必须调用 512，否则房间挂机会静默失效。

### 离房 LeaveRoomCMsg(1027)
- `f1=uid f2=token`（41B）

### 房间心跳 RoomHeartBeatCMsg(1028)（仅房主/建房时发送）
- `f1=uid f2=token f3=嵌套`，约每秒 1 次：
  ```
  f3 = {
    f1 = { f1=seq递增(1,2,3..)  f2=60  f3=seq*60+1(61,121,181..)  f4=2  f5=60 }
    f2 = 20B 状态数据（空闲时固定 0000000000000000ffffffffffffffff000000e0）
  }
  ```
- 实测 seq 从 1 起每次 +1，f3.cum = seq*60+1。

### 房间状态轮询 RoomStatusCMsg(1092)（进房后每约 1.6 秒 1 次）
- `f1=uid f2=token f3=seq递增(1,2,3..)`（43B）。
- 无 SRAM 存档时响应 `f1=-114 f2="SNAPSHOT_NO_SRAM"`；存档过旧时 `f1=-98 f2="SNAPSHOT_TOO_OLD"`——均属正常，不影响在线。
- 部分响应（264 信封）返回房内玩家 SRAM 数据（GetRoomSramMemCard 成功）。

### 挂机会话时序
- **建房挂机**（h2_stream50）：进房(1026) → [1028 每秒 + 1032/1092 每1.6秒] × N → 离房(1027)。
- **观战他人房间**（hang_spectate）：进房(1026 f3=roomId) → [1032(f6=seq) + 1092(f3=seq) 每1.6秒] × N（**无 1028 心跳**）→ 离房(1027)。
- **建房失败→观战→上位挂机**（join3 pcap，实测时序）：
  1. 建房(1026 f4=1) 选中的房间号被占用 → 服务器响应 f3=slot=255（观战），本机出现在 obList 而非 playerList；
  2. **观战期**：仅 [1032 + 1092 每1.6秒] 轮询（**无 1028 心跳**）；
  3. **上位**：发 `1033 {f1=uid f2=token f3=1}`（slot=1）→ 服务器返回 264 空体确认 → 约 2s 后客户端恢复 **1028 心跳流**（1028+1042+1092）；
  4. 判定"上位成功"= 1033 请求无错误返回（响应为空体即 ret 缺省=成功），随后与建房挂机同样进入心跳循环；
  5. **上位失败** → 保持观战挂机（1032 快照轮询 + 1092 状态轮询，无 1028），观战时长同样计入战队任务；房主退出时观战者会被踢出房间（轮询连续失败即重建房间）。

### 进入挂机区（516/517）
- 客户端进区顺序：`516 QueryZoneInfoCMsg`（查区信息）→ `517 EnterZoneCMsg`（进区）→ `1024`（玩家列表）→ `1025`（房间列表）。
- **区配置 JSON 在 517 响应 f9**（不在 1024！）：`{"Name":"挂机区","Id":1,"Limit":200,"MaxRoomNum":500,"Slot":2,"VersionList":[{"Name":"fc_8bit","Title":"演奏起来","Id":1,"Rom1Md5":"dc06..."},...]}`。
  - 实测挂机区 Name="挂机区"（另一次抓包 Name="自由区"），f4=2 为 zoneId；517 响应 f3=对局服务器 `45.120.103.100:61001`。
  - 挂机区可用游戏版本：fc_8bit(演奏起来)、fc_badap(烂苹果)、fc_mmc5(FC音乐50合1)、fc_8bitmpe(8bit音乐Encore)、fc_ktv1(ktv金曲)、fc_rickroll(瑞克摇摆)、fc_8bitrlw(8位节奏乐园)。

## 待确认

- [x] 脚本实测登录闭环 → **通过**（verify_login_flow.py）
- [x] 挂机程序 Go 实现 → **通过**（server/，登录/在线保持/任务领取跑通，签到待明日验证）
- [x] PlayerCheckInCMsg.checkInType 值 → 实测=2（DAILY_SIGN_IN 或 MONTH_SIGN_DAY_NUM 待反汇编定序）
- [x] 签到触发方式 → H2 网关 `PlayerCheckInCMsg(1175)`；弹窗状态不走网关
- [x] 在线时长经验统计方式 → 服务器自动累计（MissionListSMsg 状态变化），RefreshInfoCMsg(515) 仅保持会话
- [ ] 商城礼包接口（gotvgmall.gotvg.cn）
