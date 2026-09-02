---
name: protocol-capture-reverse
description: 游聚（X-Zone / youjuhang）私有协议的抓包逆向方法论，也可用作其他私有 TCP/HTTP2 协议的通用分析流程。当用户需要抓取客户端流量、解析 pcap、还原 protobuf 字段、定位"程序行为与真实客户端不一致"的原因、或验证某个协议假设时应使用本 skill。涵盖 tshark 后台抓包、HTTP/2 与二进制通道解析、Frida socket hook、以及对照实验验证法。
---

# 私有协议抓包逆向

## 用途

把「客户端能跑通、自己的程序跑不通」这类问题，通过抓包 → 解析 → 对照实验 → 固化的
闭环解决掉。本 skill 沉淀了游聚项目 2026-08-26 ~ 08-29 的完整逆向过程与踩坑记录。

## 何时使用

- 程序调用某个接口被拒，但不知道为什么
- 需要搞清楚某个消息的字段含义、消息之间的先后顺序
- 服务器返回了错误码，需要知道它代表什么
- 需要确认「某段流量到底是加密的还是可以解析的」
- 要为新功能抓包取证（如新增商城、新任务类型）

## 核心原则

**先实测，再下结论。**

本项目最惨痛的教训：注释里写着「1025 响应是强加密数据，Go 端无法解析」，
于是一直靠随机盲猜房间号。实际亲手发一次请求就发现是明文，一下就解析出 187 个房间。
否定性结论（"这做不到" / "这是加密的"）代价极高，必须亲手验证。

## 工作流

### 第 1 步：抓包

```bash
python scripts/capture.py list          # 先确认网卡编号，接口号会变
python scripts/capture.py start D:/Git/YoujuHang/captures/<场景>_<日期>.pcapng 5 "tcp or port 53" 50
# 操作客户端，复现目标行为
python scripts/capture.py stop
```

- `rotate_mb` 建议 25~50，长时间抓包必须轮转，否则单文件过大难处理
- 抓之前先想清楚要复现什么动作，抓完立刻记录「做了什么操作」，
  否则几小时后面对 7 GB 文件无从下手
- 命名带场景和时间戳，便于以后引用为证据

### 第 2 步：建立全景，别急着钻细节

先把抓包里有什么看清楚：

```bash
# 涉及哪些对端、哪些端口
tshark -r cap.pcapng -T fields -e ip.dst -e tcp.dstport | sort | uniq -c | sort -rn

# HTTP/2 请求都发到哪些路径（最关键的一步）
tshark -r cap.pcapng -Y http2 -T fields -e http2.header.value
```

**先列 `:path` 分布**。本项目踩过的坑：516/517 走 `/game/`、1024~1092 走 `/zone/2/`，
同一条连接、同一套封帧，走错路径就恒定返回 `UNHANDLED_MSG_ID`。

### 第 3 步：解析

HTTP/2 通道（`/login/`、`/game/`、`/zone/2/`）：

```bash
python scripts/h2_flow.py <pcap> <服务器IP> --only-data      # 消息时序
python scripts/h2_flow.py <pcap> <服务器IP> --pb             # 展开 protobuf
```

输出形如：

```
tcp.stream=67  153.99.234.163:18141 <-> 192.168.80.205:5140  client=192.168.80.205
t=141.898 C->S DATA h2stream=3 b64len=64 payload=46B msgid=516(0x0204)
t=154.516 C->S DATA h2stream=5 b64len=60 payload=45B msgid=1026(0x0402)
t=155.049 C->S DATA h2stream=7 b64len=64 payload=46B msgid=1080(0x0438)
```

自定义二进制通道（18140 / 18180）：

```bash
python scripts/msg_timeline.py <pcap> 18140 --len4 --pb
```

单独解一段 hex：

```bash
python scripts/pb_decode.py "08f6ffffffffffffffff01121057524f4e475f47414d455f544f4b454e"
# → f1 = 18446744073709551606 (i64=-10)
#   f2 = "WRONG_GAME_TOKEN"
```

解析要点：

- 请求体常带 2 字节 MsgID（小端）前缀，响应**不带**，靠 HTTP/2 stream 号关联
- base64 要同时试标准与 URL-safe、带填充与不带填充
- 大包可能被 zlib 压缩；还有 `+` 前缀 + zlib 的形态
- **成功响应与失败响应的结构可能不同**，两种都要 dump 出来对比

### 第 4 步：对照实验（最关键）

抓包只能告诉你客户端怎么做的，不能直接告诉你哪一步是必需的。
写一个探针程序，用项目的 `gate.Client` 直接对服务器发消息，**控制变量**：

```
变体A：登录 → 516 → 517 → 1026       → 失败
变体B：登录 → 512 → 516 → 517 → 1026 → 成功
```

一次只改一个变量。本项目靠这个方法定位出「漏发 512 导致所有房间消息
`-10 WRONG_GAME_TOKEN`」——这个问题靠读代码和看抓包都没找出来。

探针的形态：在 `server/cmd/` 下建临时 main 包，登录 → 逐条发消息 → dump 原始 hex
与解析结果。用完整日志（含每个响应的 errStr），不要只看 ret。

### 第 5 步：区分「传输失败」与「业务返回码」

HTTP 200 不代表业务成功。私有协议必须校验业务返回码。

更要紧的是**反过来**的坑：很多返回码是「正常但暂无数据」，若默认「非 1 即失败」，
会把正常挂机误判为掉线。做法是先枚举一遍正常路径会返回哪些非 1 的码，
再把它们显式列为非失败。

查 `references/error_codes.md`，特别注意：

- `-10 WRONG_GAME_TOKEN` / `-19 ROOM_NOT_FOUND` → 致命，需重建会话
- `-142 SNAPSHOT_NO_SRAM`（1092 恒定返回）、`-34 SNAPSHOT_TOO_OLD`、
  `-29 NOT_PLAYING`、`-35 SLOT_OCCUPIED` → **正常**，不能当失败

响应长度可反推错误内容，日志只有 `bodyLen` 时也管用：

```
len = 11(f1) + 2 + len(errStr) [+ 3 if f3]
"WRONG_GAME_TOKEN"(16B) → 29 字节
```

### 第 6 步：固化

改完代码，把结论写回去，注明**抓包证据**（pcap 文件名 + 日期 + 实测数值）：

```go
// 更正（2026-08-29 实测）：1025 响应**不是**强加密数据。
// 请求体只带 f1=uid f2=token 时返回明文 protobuf，实测 187 个房间。
// 此前「强加密无法解析」的判断导致长期只能随机盲猜房间号。
```

同时更新 `docs/protocol_notes.md` 与 `references/` 下的表。
**特别注意：旧的否定性结论要主动改掉，否则会继续误导。**

## Frida：当流量被加密时

Wireshark 只能看到密文时，在 `send`/`recv` 层截获明文：

```bash
python scripts/frida_hook_run.py X-Zone.exe D:/tmp/hook.log      # attach 到已运行进程
python scripts/frida_hook_run.py 12345 D:/tmp/hook.log --spawn   # 需抓启动阶段时
```

`send` 在 onEnter 读，`recv` 在 onLeave 读，WSABUF 指针 64 位在 +8、32 位在 +4
——详见 `scripts/frida_socket_hook.js` 头部注释。

## 参考资料

按需加载，不要一次全读：

| 文件 | 何时读 |
|---|---|
| `references/protocol_map.md` | 需要消息 ID、字段结构、通道与封帧格式时 |
| `references/error_codes.md` | 遇到不明返回码，或要判断该码是否算失败时 |
| `references/case_studies.md` | **排查疑难问题前先读**。7 个真实案例与贯穿性方法论 |
| `references/tooling.md` | 需要环境路径、tshark 字段、现有抓包资产清单时 |

## 排障检查清单

卡住时按顺序过一遍：

- [ ] 请求发到正确的 `:path` 了吗？（`/game/` vs `/zone/2/`）
- [ ] base64 用的是 URL-safe 吗？
- [ ] 响应体的 MsgID 前缀 / zlib 处理对了吗？
- [ ] 前置消息序列完整吗？（漏了哪条「看起来不重要」的消息？）
- [ ] 是不是把正常的非 1 返回码当成了失败？
- [ ] HTTP 200 但业务 ret 是负数——校验返回码了吗？
- [ ] 文档里有没有「这是加密的 / 做不到」的断言，值得亲手验证一次吗？
- [ ] 失败分支里，是否需要先做清理动作（如离房）再重试？
