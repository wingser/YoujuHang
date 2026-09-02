# 返回码对照表

服务器以 `f1 = ret` 下发，类型为 uint64 补码（负数错误码）。
Go 侧解析时注意：`uint32(proto.GetVarint(f, 1))` 后 `-10` 表现为 `4294967286`。

## 一、判定方法

只有 **token 失效** 和 **房间不存在** 是致命的（会话/房间已不可用，必须重建）；
其余一律视为「正常但暂无数据/不符合条件」，**绝不能当作断线**，否则会把正常挂机
误判为掉线并反复重建。

```
致命（需重建会话）：-10 WRONG_GAME_TOKEN、-19 ROOM_NOT_FOUND
正常（保持现状）：  -24、-29、-34、-35、-87、-98/-114、-142 等
```

## 二、已实测确认的返回码

| ret | 名称 | 含义 | 处理 |
|---|---|---|---|
| 1 | — | 成功 | — |
| **-10** | `WRONG_GAME_TOKEN` | 会话 token 未被网关接受 | **致命**。通常漏发了 512 进大厅，或会话失效 |
| **-19** | `ROOM_NOT_FOUND <id>` | 房间号不存在 | **致命**。房间号必须取自 1025 列表 |
| -24 | `ALREADY_IN_ROOM` | 已在房间内重复进房 | 正常。先离房(1027)再操作 |
| -29 | `NOT_PLAYING` | 观战身份发了 1028 心跳 | 正常。观战不发心跳，靠 1032 保活 |
| -34 | `SNAPSHOT_TOO_OLD` | 1032 快照轮询首帧 | 正常。下一帧即 ret=1 |
| -35 | `SLOT_OCCUPIED <uid>` | 1033 上位时座位被占 | 正常。保持观战 |
| -87 | `WRONG_ROOM_STATUS <id> RS_GAMING` | 建房时该号已被占用且在游戏 | 正常。换号 |
| **-142** | `SNAPSHOT_NO_SRAM` | 1092 状态轮询无存档 | **正常且恒定**。房主与观战都会一直收到 |

## 三、其他通道见过的返回码

| ret | 名称 | 出现位置 |
|---|---|---|
| **-3** | `PASSWORD_NOT_MATCH<32hex>` | **登录（密码错误）**。errStr 为 `PASSWORD_NOT_MATCH` 后紧跟一段 32 位十六进制（实测 `PASSWORD_NOT_MATCH9507d6df7a5f2532730f769d9cc1e6f3`），含义未确认（疑似密码或挑战值哈希）。**2026-09-01 实测确认** |
| -2 | `ACCOUNT_NOT_FOUND` | 登录（账号不存在） |
| -14 | `UNHANDLED_MSG_ID zone 0x204/0x205` | 516/517 误发到 `/zone/2/` |
| -18 | `ZONE_NOT_FOUND <a> <b> <zoneId>` | 517 进区，区 ID 无效 |
| -23 | `point not meet` | 签到（已签到或点数不足） |
| -127 | `MISSION_ALREADY_FINISH <id>` | 1174 领奖，已领过（幂等） |
| -128 | `MISSION_NOT_ACCOMPLISH <id>` | 1174 领奖，未达成 |

## 四、数值存疑处（务必以实测为准）

`docs/protocol_notes.md` 中记录 1092 返回 `-114 SNAPSHOT_NO_SRAM`、`-98 SNAPSHOT_TOO_OLD`，
而 2026-08-29 实测为 **-142** 与 **-34**。两者不一致，可能服务端改过，也可能旧记录解码有误。
**遇到与本文不一致的数值时，以当次实测为准并更新本表。**

## 五、错误响应体结构

```
f1 = ret（uint64 补码）
f2 = errStr（形如 "WRONG_GAME_TOKEN" 或 "ROOM_NOT_FOUND 250"）
f3 = 部分错误附带（如 ALREADY_IN_ROOM 时 f3=254）
f4 = 少数错误会附带 RoomInfo（如 WRONG_ROOM_STATUS 会带回当前房间信息）
```

据此可反推响应长度，用于校验解析是否正确：

```
len = 11(f1) + 2 + len(errStr) + [3 if f3]
例 "WRONG_GAME_TOKEN" (16B) → 11 + 2 + 16 = 29 字节
例 "ROOM_NOT_FOUND 250" (18B) → 11 + 2 + 18 + 3 = 34 字节
```

日志里只看到 `bodyLen=29` 时，就能反推出错误是 `WRONG_GAME_TOKEN`——
这是在没有完整 body 时定位问题的实用技巧。
