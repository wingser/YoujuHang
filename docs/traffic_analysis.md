# 抓包与封帧分析记录

> 环境：Windows + Npcap + Wireshark(tshark)
> 抓包文件：`captures/session_login_00001_20260826121526.pcapng`
> 日期：2026-08-26

## 一、抓包环境

- 网卡：WLAN 4（接口 5），默认网关 192.168.224.205
- 抓包工具链：
  - `tools/capture_ctrl.py` —— tshark 启停控制（start/stop/list/status）
  - `tools/pcap_analyze.py` —— pcap 会话/DNS/HTTP/载荷提取
  - `tools/frame_decode.py` —— HTTP/2 帧 + protobuf 解析
  - `tools/analyze_h2_flow.py` —— 按 IP 逐流解析 HTTP/2 帧序列
- 抓包 BPF：`tcp or port 53`，文件轮换 25MB

## 二、DNS 解析结果（游聚服务器）

| 域名 | IP |
|---|---|
| gamelogin3.gotvg.com | 140.210.17.134 |
| gotvg.com (www) | 112.92.61.2 |
| gotvgmall.gotvg.cn | 116.172.76.136 |
| gotvg.cn | 61.147.93.140 |
| 游戏网关（登录响应下发） | 53.99.23.163:18141 → 实际 153.99.234.163:18140/18141 |

## 三、登录流程（18000 端口 = HTTP/2）

`gamelogin3.gotvg.com:18000` 是 **HTTP/2** 连接（自实现帧格式，标准 HTTP/2 帧头）。

### 帧格式（HTTP/2）
- 连接前言：`PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n`（24 字节）
- 帧头 9 字节：`[length:3][type:1][flags:1][stream_id:4]`
- type: 0x00=DATA, 0x01=HEADERS, 0x04=SETTINGS, 0x06=PING, 0x07=GOAWAY, 0x08=WINDOW_UPDATE

### 客户端登录消息（stream 3）
```
HEADERS(len=76, flags=0x04)  密文（76 字节，以 0x41 开头，会话固定）
DATA(len=100, flags=0x01)    base64(protobuf)
```
DATA base64 解码（去掉 1 字节前缀 `00` 后为 protobuf）：
```
00 01   <- 前缀：00 + 消息类型 varint 1（登录）
protobuf:
  field 2: 用户名   = "houyoku"
  field 3: 机器码   = "16D7A4FCA7442DDA3AD93C9A726597E4" (32 hex)
  field 4: MAC      = "54-05-DB-91-34-AB"
  field 6: varint   = 大随机数/时间戳
```

### 服务器登录响应（stream 3）
```
HEADERS(len=49, flags=0x04)  密文（49 字节，以 0x88 开头，会话固定）
DATA(len=92, flags=0x01)     base64(protobuf)
```
DATA 解码：
```
protobuf:
  field 1: varint = 1
  field 3: uid    = 6017780 (0x5bd2f4)
  field 4: key    = "99313bf26fd26b29259f43c23ed18559" (32 hex 会话密钥)
  field 5: { field 1: "53.99.23.163:18141", field 2: 1 }   <- 网关地址
```
响应后服务器发 GOAWAY 关闭登录连接。

## 四、网关连接（18141 = HTTP/2）

客户端连接登录响应中的网关地址（153.99.234.163:18141）。

### 验证消息（stream 3）
```
HEADERS(len=76, flags=0x04)  密文 —— 与登录请求 HEADERS 完全一致
DATA(len=60, flags=0x01)     base64:
  00 02   <- 前缀：00 + 消息类型 varint 2（网关验证）
  protobuf:
    field 1: uid  = 6017780
    field 2: key  = "99313bf26fd26b29259f43c23ed18559"
    field 3: varint = 时间戳?
```
服务器响应：HEADERS(49B 密文，与登录响应一致) + DATA(252B base64 大消息)

### 业务消息（stream 5/7/9/...）
每个业务请求 = HEADERS(密文, 6-15 字节) + DATA(base64 protobuf, 带 `00 02` 等前缀)。
客户端 HEADERS 密文前 5 字节随请求递增（计数器）。

### 心跳（HTTP/2 PING）
- type=0x06 (PING)，payload 8 字节随机数
- 客户端发 PING(flags=0x00)，服务器回 PING ACK(flags=0x01)
- **间隔 10 秒**（实测 t=50.06→60.14）

## 五、大厅连接（18140 = 自定义二进制协议）

非 HTTP/2，独立连接 153.99.234.163:18140。

### 客户端验证消息
```
[4字节小端长度] + payload
例：27 00 00 00 | 00 09 00 08 f4 a5 ef 02 12 20 "99313bf26fd26b29259f43c23ed18559"
```

### 心跳
```
客户端 -> 服务器：00 00 00 00 25 02 00（7 字节）
服务器 -> 客户端：02 00 00 00 26 02 00 08 01（9 字节）
间隔 5 秒
```

### 服务器推送
- 消息前缀 `91 09 00 08` 固定 + 消息 ID + protobuf 载荷
- 内容：玩家上线/房间数据等

## 六、未解问题

1. **HEADERS 密文加密算法**（关键）：
   - 客户端 76B 密文（`41 8f 0b 6c ...`）与服务器 49B 密文（`88 5f 92 49 ...`）在登录/网关会话中固定
   - 业务请求 HEADERS 密文短且含递增计数器
   - 不同响应共享 20 字节密文片段 → 流密码特征
   - 需要逆向 X-Zone.exe 加密逻辑（`md5_encrypt`）
2. **签到消息**：本次抓包未捕获签到流量（签到走 TCP 协议，需再次抓包触发）
3. **在线时长上报**：`MissionSubmitCMsg_DailyOnline` 消息尚未抓取

## 七、已确认的 protobuf 消息字段（字符串表）

```
Proto.LsLoginCMsg: account, mac, password
Proto.LsLoginSMsg: token, errStr
Proto.CheckXzoneSignReq: token, md5, md5_encrypt
Proto.CheckXzoneSignAck: err_str
Proto.AddXzoneSignReq: token, md5
Proto.AddXzoneSignAck: err_str, md5_req, md5_ack
Proto.MissionSubmitCMsg_DailyOnline
Proto.ProtobufAntiDisruptOnLoginSuccessUploadHardwareInfoReq:
    mac, machine_id, token, nickname, bios_uuid
Proto.ProtobufAntiDisruptOnArrestUploadHardwareInfoReq: 同上
```

## 八、客户端网页入口

`http://client.gotvg.com/?token=<session_key>&userid=<uid>`
—— token + userid 鉴权，可复用于商城/网页功能。
