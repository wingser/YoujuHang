# 工具链与环境

## 一、环境（本机 Windows，用户 Lenovo）

路径统一维护在项目 `tools/tool_paths.ini`，新增脚本应从那里读取，不要硬编码。

| 工具 | 路径 / 版本 |
|---|---|
| tshark / Wireshark | `D:\Program Files\Wireshark\tshark.exe` |
| dumpcap | `D:\Program Files\Wireshark\dumpcap.exe` |
| Go 1.21.13（默认，兼容 Win7） | `D:\Git\tools\go-sdk\go121\bin\go.exe` |
| Go 1.24.3（备用） | `D:\Git\tools\go-sdk\go\bin\go.exe` |
| rsrc（manifest/图标） | `C:\Users\Lenovo\go\bin\rsrc.exe` |
| Python | 系统 PATH 中的 `python` |
| frida | Python 包，17.17.0 |

抓包网卡：WLAN 4（tshark `-D` 里的接口 5），默认网关 `192.168.224.205`。
**接口编号会变**，每次抓包前先 `capture.py list` 确认。

## 二、tshark 常用字段

本 skill 的脚本统一用 `-T fields` 提取，常用字段：

```
-e frame.number          帧号
-e frame.time_relative   相对首帧的时间（秒）—— 做时序分析最有用
-e ip.src / ip.dst       源/目的 IP
-e tcp.srcport / tcp.dstport
-e tcp.stream            TCP 流号（一条连接一个号，分组首选）
-e tcp.flags             标志位（0x02=SYN 0x10=ACK，判方向用 SYN 且非 ACK）
-e tcp.len / tcp.payload 载荷长度 / 载荷 hex
-e http2.type            HTTP/2 帧类型（需开启 HTTP/2 解析）
-e http2.header.value    HPACK 解码后的 header 值
```

常用显示过滤器（`-Y`）：

```
tcp.port==18141 && tcp.len>0          某端口带载荷的报文
tcp.flags.syn==1 && tcp.flags.ack==0  握手发起方（判客户端）
http2                                 只看 HTTP/2
dns                                   DNS 查询（找域名→IP 映射）
```

抓包过滤器（`-f`，BPF，在内核态过滤，性能好）：

```
tcp or port 53      # 项目常用
host 153.99.234.163 # 只抓某个服务器
```

## 三、项目现有抓包资产

`d:\Git\YoujuHang\captures\` 下约 221 个 pcapng、7.7 GB，命名规则：

```
<场景>_<日期>_<轮转序号>_<本段起始时间戳>.pcapng
例：join3_20260827_00001_20260827152918.pcapng
```

场景分组：`session_login_*`（登录）、`join2_*`/`join3_*`（进房挂机两路并行）、
`mall_*`（商城）、`team_*`/`team_task_*`/`team_contribute_*`（战队）、
`exit_test_*`（退出测试）。

关键单文件样本：

| 文件 | 大小 | 说明 |
|---|---|---|
| `hang_spectate_20260827.pcapng` | 393 MB | 完整观战挂机会话，进区时序的主要证据 |
| `join_room_20260827_00001_20260827152008.pcapng` | 5.6 MB | 短样本，适合快速验证脚本 |
| `team_contribute_20260827.pcapng` | 208 MB | 战队捐献 |

导出的中间产物（同目录）：`h2_stream20.txt`（516/517 进区）、
`h2_stream50.txt`（建房/心跳/离房）、`join3_18141_timeline.txt`（3791 帧时序）、
`sslkeys.log`（TLS keylog）。

## 四、Frida 用法要点

Windows 本机进程直接 attach（推荐，可等客户端登录后再挂，避免错过初始化）：

```bash
python scripts/frida_hook_run.py X-Zone.exe D:/tmp/hook.log
python scripts/frida_hook_run.py 12345 D:/tmp/hook.log      # 按 PID
python scripts/frida_hook_run.py X-Zone.exe D:/tmp/hook.log --spawn   # 需抓启动阶段时
```

进程名要带 `.exe`；不确定时用 `tasklist` 确认。

hook 点与设计要点见 `scripts/frida_socket_hook.js` 头部注释，核心三条：

1. `send` 在 **onEnter** 读（数据在缓冲区里，此时有效）
2. `recv` 在 **onLeave** 读（只有返回后才知道实际收到多少字节）
3. WSABUF 指针在 64 位下是 `+8`、32 位下是 `+4`，用 `Process.pointerSize` 区分

## 五、Go 侧对应的实现位置

逆向出的协议最终落在：

| 位置 | 内容 |
|---|---|
| `server/internal/protocol/msgid.go` | 消息 ID 常量与请求构造函数 |
| `server/internal/proto/proto.go` | 极简 protobuf 编解码 |
| `server/internal/gate/` | HTTP/2 客户端，`/game/` 与 `/zone/2/` 入口 |
| `server/internal/room/room.go` | 进区/建房/挂机循环 |
| `server/internal/session/session.go` | 登录与 512 进大厅 |

写探针验证协议时可参考 `server/cmd/` 下自建的临时 main 包，
用项目的 `gate.Client` 直接对服务器发消息，最快。
