# 技术文档

> 面向开发者与维护者。功能说明与使用方式见 [README](../README.md)。

---

## 一、架构总览

```
┌─────────────────────────────────────────────────────────────┐
│  cmd/youjuhang          入口：参数解析、信号、托盘、Web 启动   │
│  cmd/guard              守护进程：监视心跳，崩溃/假死自动拉起   │
│                         （可选组件，缺失时主程序独立运行）      │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│  internal/core          Manager：每账号一个 goroutine        │
│  · 账号生命周期（启动/停止/重登）                             │
│  · 状态机（online / stopped / relogin_wait / login_fail）     │
│  · 在线侦查（借助其他账号探测目标是否在线）                    │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│  internal/biz           Worker：单账号业务主循环              │
│  · 在线刷新(515)  · 任务列表(1172)  · 领奖(1174)              │
│  · 签到(1175)     · 战队详情(596/3) · 捐献(596/15)            │
│  · 房间挂机调度   · 跨天重置                                  │
└──┬────────────┬────────────┬────────────┬───────────────────┘
   │            │            │            │
┌──▼───┐   ┌────▼────┐  ┌────▼────┐  ┌────▼────┐
│session│  │  gate   │  │  room   │  │  mall   │
│ 登录  │  │ HTTP/2  │  │ 分区房间 │  │ 商城礼包 │
└──┬────┘  └────┬────┘  └────┬────┘  └─────────┘
   │            │            │
   └────────────┼────────────┘
                │
        ┌───────▼────────┐
        │ protocol + proto│  消息 ID / 封帧 / 极简 protobuf
        └────────────────┘
```

**分层原则**：`biz` 是业务编排层，不直接碰网络；网络细节封在 `gate`（HTTP/2 通道）与 `room`（分区房间）；`protocol`/`proto` 负责编解码。

---

## 二、目录结构

> 标注「**不入库**」的由 `.gitignore` 排除：本地保留、不推送。详见 6.4 节。

```
YoujuHang/
├── server/                      主程序（Go）
│   ├── cmd/
│   │   ├── youjuhang/           主入口
│   │   ├── allinone/            调试用：单账号全流程
│   │   ├── diagwin7/            Win7 兼容性诊断
│   │   └── hello/               最小连通性测试
│   ├── internal/
│   │   ├── biz/                 业务层（1325 行，最核心）
│   │   │   ├── biz.go           主循环、任务、签到、挂机调度
│   │   │   ├── contribute.go    战队捐献 + 战队详情判定
│   │   │   └── social.go        好友协议（在线侦查）
│   │   ├── core/                多账号管理器
│   │   ├── session/             登录状态机
│   │   ├── gate/                HTTP/2 客户端（/game/ 与 /zone/2/）
│   │   ├── room/                分区/房间挂机（737 行）
│   │   ├── mall/                商城礼包
│   │   ├── web/                 Web 控制台（HTTP API + 内嵌 UI）
│   │   ├── tray/                系统托盘（windows only；app.ico 经 go:embed 内嵌）
│   │   ├── config/              YAML 配置
│   │   ├── protocol/            消息 ID 常量与构造器
│   │   ├── proto/               极简 protobuf 编解码
│   │   └── logx/                按账号独立日志
│   ├── configs/accounts.yaml    配置样例（**已脱敏**，账号/密码/MAC 均为占位）
│   ├── build.bat                构建脚本（win64 + win32）
│   ├── build_linux.bat          Linux 构建脚本
│   ├── app.manifest             Windows manifest（Common Controls 6.0，托盘必需）
│   ├── go.mod / go.sum
│   └── dist/                    构建产物 —— **不入库**
├── docs/                        协议与分析文档
├── captures/                    抓包文件（约 12.8GB）—— **不入库**
├── proto/                       .proto 定义
├── tools/                       分析脚本与工具路径配置
└── .codebuddy/skills/           逆向方法论 Skill：抓包→解析→对照实验→固化
```

---

## 三、协议层

### 3.1 通道

| 通道 | 路径 | 消息 |
|---|---|---|
| 登录 | `POST http://gamelogin3.gotvg.com:18000/login/` | 256 / 258 |
| 业务 | `POST http://<gate>/game/` | 512 / 514 / 515 / 596 / 1172 / 1174 / 1175 / 1176 |
| 分区房间 | `POST http://<gate>/zone/2/` | 1024~1092 |

**关键坑**：业务与房间消息**共用同一条 HTTP/2 连接与同一个 gate 地址**，仅路径不同。早期把 516/517 发到 `/zone/2/` 导致恒定返回 `UNHANDLED_MSG_ID`，浪费数日。

### 3.2 封帧

- **请求体**：`base64url( [MsgID:2字节 小端] + protobuf )`
- **响应体**：`base64url( protobuf )`，**无 MsgID 前缀**，靠 HTTP/2 stream 号关联
- **596/f3=3 等大响应**：响应文本以 `+` 开头，其后为 `base64url(zlib(protobuf))`
- 部分响应经 zlib 压缩，按首字节 `0x78` 判定后统一解压

见 `gate/client.go` 的 `raw()`。

### 3.3 消息 ID

| ID | 消息 | 用途 |
|---|---|---|
| 256 / 258 | LsLogin | 登录 |
| 512 | EnterLobby | 进大厅（房间消息的前置必需步骤） |
| 514 | UserInfo | 昵称/等级/经验 |
| 515 | RefreshInfo | 在线保活（action=5） |
| 596 | TeamContribute | 战队。f3=3 详情 / f3=15 捐献 |
| 1024~1092 | Zone/Room | 区列表、进区、建房、心跳、状态 |
| 1172 | MissionList | 任务列表 |
| 1174 | MissionSubmit | 领奖 |
| 1175 | PlayerCheckIn | 签到 |
| 1176 | PlayerSocial | 查询任意 uid 在线状态（在线侦查） |

完整定义见 `internal/protocol/msgid.go`。

### 3.4 protobuf

`internal/proto` 是**手写极简实现**，只支持 varint / len-delimited / fixed64 三种 wire type，足够覆盖游聚协议，避免了引入 `protobuf-go` 与 `.pb.go` 生成步骤。

```go
type Field struct {
    Num  int    // 字段号
    Wire int    // wire type
    V    uint64 // varint 值
    B    []byte // bytes 值
}
```

---

## 四、核心模块

### 4.1 core — 多账号管理器

每个账号一个 goroutine + 独立 context，可单独取消。

**状态机**：

```
        Start                  Login 失败
  ┌──────────────┐  ┌────────────────────────┐
  │   offline    ├─►│      login_fail        │◄──┐
  └──────┬───────┘  └───────────┬────────────┘   │ 重试
         │ 登录成功              │ 成功            │ (指数退避)
         ▼                      │                │
  ┌──────────────┐              │                │
  │    online    │◄─────────────┘                │
  └──────┬───────┘                               │
         │ 会话失效（被顶号）                      │
         ▼                                       │
  ┌──────────────┐  侦查到对方离线                 │
  │ relogin_wait │───────────────────────────────┘
  └──────────────┘
         │ StopAccount
         ▼
  ┌──────────────┐
  │   stopped    │
  └──────────────┘
```

**被踢重登**：会话失效时不立即重登（会顶掉用户），而是两级策略：

**① 在线侦查（有其它账号在挂机时，零风险）**

1. 从当前 `online` 的账号中**随机**挑一个「侦查账号」，用 1176 查询目标 uid 的在线状态
2. 只读查询，不影响任何会话
3. 目标在线 → 继续等（用户在游戏）；目标离线 → 自动重登恢复挂机
4. 侦查账号自身失效 → 标记进 `failed` 并**随机换一个**，不会反复选回失效的那个

**② 兜底探测（全账号掉线，如服务器维护/整机断网）**

无可用侦查账号时，每 `fallbackInterval`（10 分钟）**随机**挑一个账号尝试登录：

- 登录失败 → 服务器仍不可用，等下一轮
- 登录成功 → 服务器已恢复，用该会话侦查目标；目标离线才恢复挂机
- 等待期间若有账号恢复在线，自动切回 ① 常规侦查（不再做登录探测）

**为什么随机而非固定第一个**（2026-08-31 改）：

| 维度 | 固定首个账号 | 随机挑选 |
|---|---|---|
| 请求压力 | 所有被踢账号都盯住同一个，风控风险集中 | 分散到所有可用账号 |
| 失效轮换 | 重挑时很可能选回刚失效的那个 | `failed` 集合保证换到别的 |
| 顶号风险 | 全账号掉线时每次都顶同一个用户 | 风险均摊到不同账号 |

兜底探测还带一条**安全约束**：优先挑非 `relogin_wait` 的账号（`offline`/`stopped`/`login_fail`，
疑似未被用户占用），仅当没有其他候选时才用 `relogin_wait` 的账号。

相关实现：`core.go` 的 `pickProbeSession` / `probeWaitRelogin` / `fallbackWaitRelogin` / `pickFallbackAccount`，
测试见 `core_test.go`（8 项，覆盖排除、轮换、随机分布、优先级、停用过滤）。

### 4.2 biz — 业务主循环

```
Run(ctx)
  ├─ 首轮：查用户信息(514) → 检测战队(596/3) → 经验头像 → 捐献 → 启动挂机 → 推送状态
  └─ 循环：跨天检测 → 刷新在线(515) → 领奖(1172+1174) → 签到(1175) → 商城 → 经验头像 → 捐献
```

经验头像（`maybeDressAvatar`）内部按日期去重，每天只真正执行一次；常态下一个
GET 请求结束，佩戴有效时立即返回，不会拖慢循环（详见 5.17）。

**刷新间隔**：`refresh_minutes` 基础上加 ±20% 随机抖动（`jitter()`），规避风控。

**挂机监督**：`hangSupervisor` 独立 goroutine，定期查战队任务，3 小时在线奖励达成后主动退出挂机并标记 `hangDoneKey`，当日不再重启。

### 4.3 room — 房间挂机

进区时序（顺序不能错）：

```
512 进大厅 → 516 查区信息 → 517 进区 → 1026 建房 → 心跳(1028, 1s) + 状态轮询(1092, 1.6s) → 1027 离房
```

**在线时长上报（1042）**：战队在线任务（2002/2003/2004）的进度由服务器累计，依赖房主身份每秒发送 1042：

- 结构：`f1=uid f2=token f3={f1=3(固定), f2=tick(递增计数器)}`
- tick 每次 +56（真实客户端每帧 +52~60），初值取 1124000 以匹配真实量级
- 服务器只校验递增与频率，不校验绝对时间
- **必须房主身份**（建房/上位成功才发），观战身份发 1032 快照保活

缺少 1042 会导致战盟任务整夜无进度——详见 `docs/zone_hang_fix.md`。

掉线由主循环每轮自动重启；挂机子 context 可独立取消。

### 4.4 web — 控制台

- 单页应用，`go:embed` 内嵌，无外部依赖
- 前端用 ES5 + XMLHttpRequest 编写，兼容 Win7 下 Chrome 109 / Firefox ESR / IE11
- 后端仅 6 个 API：`/api/status`、`/api/config`、`/api/accounts`、`/api/logs`、`/api/action`、`/api/version`

---

## 五、关键机制

### 5.1 跨天重置

`rolloverIfNeeded()` 在每轮业务之前检查日期 key 变化，新一天清零：

- 签到标记、任务领取记录、商城领取额度、战队状态缓存、挂机完成标记、当日捐献量、
  经验头像检查标记（`avatarKey`——不清则第二天起不再检查，头像过期无人察觉）

**踩坑**：早期漏清 `hangDoneKey`，导致第二天整天不挂机。

### 5.2 战队判定（重要）

`queryTeamInfo()` 用 596/f3=3 判定账号是否已加入战队。

**响应结构**（2026-08-31 三账号对照实测）：

```
顶层: f1=ret  f6=<战队数据块>

f6 子字段（有战队）: f1=战队ID  f2=公告  f3=成员列表  f4=自身UID  f5=贡献度余额
f6 子字段（无战队）:                    f3=可加入列表  f4=自身UID
```

**关键教训**：`f6.f4` **不是**"是否加入战队"的标志——它恒为**当前账号自身的 UID**，因此 `f6.f4 != 0` 恒为真。早期用该判据导致所有账号（含从未加入战队的新注册账号）都被误判为已加入战队。

**正确判据是 `f6.f1`（战队 ID）**：有战队时为该战队 ID，无战队时字段不存在。

证据：`captures/team_info_youjugua_20260831_*.pcapng`。详见 `protocol_map.md`。

### 5.3 捐献幂等

`ContributeStore` 按 uid 持久化当日已捐献量到 `contribute_state.json`，跨程序重启防重复捐献。跨天时 `ResetIfNewDay()` 作废昨日记录。

**捐献量必须是 100 的倍数**：服务器对非整百的捐献量整单拒绝，返回
`count must be a multiple of 100`。而 `amount` 会被"余额 − 下限"裁剪（不能把余额捐到下限以下），
裁剪结果通常是非整百值（余额 6250、下限 3000 → 3250）。不规整就会被拒、而 `need` 没有减少，
于是每个主循环（每分钟）重试一次——**实测一夜产生 564 条告警且毫无进展**。

现由 `roundContributeAmount()` 向下取整到 100 的倍数（不足 100 归零并退出）：
既不越过可捐上限，又避免无效重试。

### 5.4 并发与数据竞争

本项目踩过的最严重事故：子 goroutine panic 导致**进程无声消失**。

原因与对策：

| 风险点 | 对策 |
|---|---|
| `w.tasks` 主循环写、`buildTasks` 读 | 统一由 `statsMu` 保护，读时先取快照 |
| `w.roomSince` (time.Time) 并发读写 | 由 `roomMu` 保护（撕裂读会得到非法 loc 指针） |
| 子 goroutine panic 终结进程 | 所有长期 goroutine 挂 `defer recoverGoroutine()` |
| `-H windowsgui` 下 stderr 被丢弃，panic 无痕 | panic 捕获后写入日志文件 |

**`-H windowsgui` 是关键约束**：无控制台窗口，stderr 被丢弃，任何未捕获的 panic 都表现为「进程消失、日志无痕」。主 goroutine 的 `recover` 管不到子 goroutine，**必须每个子 goroutine 单独挂 recover**。

### 5.5 挂机有效期

账号可设定挂机天数，到期后自动停止。核心字段 `config.Account.ExpireDate`（`YYYY-MM-DD`，空=永久）。

**语义（按日期比较，不比较时刻）**：

| 场景 | 结果 |
|---|---|
| `ExpireDate` 为空 | 永久有效 |
| 当日 ≤ 到期日 | 未过期（到期日当天 23:59 前仍可挂机） |
| 当日 > 到期日 | 已过期 |

例：8/31 新增填 3 天 → `ExpireDate=2026-09-03`，8/31~9/3 可挂机，9/4 起过期。

**三个关键函数**（`internal/config/config.go`）：

| 函数 | 用途 |
|---|---|
| `CalcExpireDate(now, days)` | 新增：今天 + days；days≤0 返回空（永久） |
| `ExtendExpireDate(old, today, days)` | 编辑：基数取「原到期日」还是「今天」取决于是否已过期（见下表） |
| `IsExpired(expire, now)` | 判定；格式非法按永久处理（避免误停账号） |

**编辑语义（2026-09-02 修订）**——输入值是**增减量**，不是新天数；
基数取「原到期日」还是「今天」，取决于账号当前**是否已过期**：

| 账号状态 | `days > 0` | `days == 0` | `days < 0` |
|---|---|---|---|
| 未过期（含到期日当天） | 原到期日 + days | 原到期日（不变） | 原到期日 + days |
| 已过期 | **今天 + days** | **今天**（拉回今天） | 原到期日 + days |
| 长期（空 / 格式非法） | 今天 + days | 原样返回 | 原样返回 |

**为何已过期账号改以今天为基数**（2026-09-02 需求调整）：

旧行为一律从原到期日累加，导致**已过期**账号填正数后算出的仍是过去日期——
账号依旧不可用，等于"续期了个寂寞"。因此：

- `days > 0`：以**今天**为基数。例：一周前过期、今天填 `1` → 到期日为明天。
- `days == 0`：有效期拉回**今天**（今天仍可挂机，明天起过期），相当于"复活一天"。
- `days < 0`：**仍以原到期日为基数**向前减（缩短语义不因过期而改变）。

未过期账号沿用旧语义（一律以原到期日为基数），保证「+30 再 -30 回到原值」可逆，
不让已购天数凭空蒸发。

> **前后端必须同步**：`internal/web/ui/index.html` 的 `onEdDaysInput()` 实现了
> **同一套规则**用于实时预览。改任一方都必须同步另一方，否则 UI 预览与实际结果不符。
> 判定口径统一为：剩余天数 `< 0` 即已过期（到期日当天为 `0`，未过期）。

**整数约束**：天数只接受整数。前端用 `/^-?\d+$/` 校验（3.5 直接报错而非静默截断），
后端 `json` 反序列化到 `int` 时小数会失败，统一转成「天数必须是整数」提示；
并限制范围 ±3650 天（约 10 年），防止手滑输入天文数字。

**行为矩阵**：

| 触发点 | 未过期 | 已过期 |
|---|---|---|
| 程序启动 / 全部启动 | 正常启动 | 跳过，返回跳过数量 |
| 单账号启动 | 正常启动 | 返回 `ErrAccountExpired`，前端弹确认后可 `force` 强制启动 |
| 运行中跨过到期日 | 继续 | 每小时检查一次并自动停止 |

**设计要点**：

- **编辑是增减而非重算**：以配置中现有到期日为基数做加减，即便账号已过期也保留原到期日，不让已购天数凭空蒸发；正负操作可逆（+30 再 -30 回到原值）。
- **过期判定实时计算**：`accountRuntime.snapshot()` 每次从 `acc` 重算 `Expired`，跨天无需重启即可生效。
- **到期停止走独立 watcher**：`expireWatcher` 每小时扫描一次（到期粒度是天，无需高频）。停止操作先收集名单再释放锁后执行——`StopAccount` 内部要获取 `m.mu`，持锁调用会死锁。

### 5.6 重复账号拦截

新增账号**不允许重名**，含仅大小写不同的变体。

- **校验位置**：`Manager.AddAccount` 调用 `findByNameLocked(name)`（忽略大小写的 `strings.EqualFold`），命中已存在账号即返回 `账号 %q 已存在，不能重复添加`。
- **为何连大小写也拦**：服务端通常把 `abc` 与 `ABC` 识别为同一用户，若两者同时挂机，会互相顶下线，表现为反复掉线重连。宁可拒绝，也不让它们并存。
- **前端双保险**：`addAccount` 在提交前先用 `knownNames` 做一次忽略大小写的即时查重，提前给出「账号 xxx 已存在，不能重复添加」提示；**服务端仍是权威校验**（最终兜底，防止绕过前端）。
- **删除后可重加**：`RemoveAccount` 从 `states` 与 `cfg.Accounts` 中一并删掉，之后可重新添加同名账号。
- **配置手工重名**：`syncFromConfig` 按忽略大小写检测 `accounts.yaml` 中的重复项，告警（后者覆盖前者）但不阻断启动。

### 5.7 单账号并发登录防护

**问题（2026-09-01「启动后一直卡在登录中」）**

`startLocked` 原先只调用 `old.cancel()` 发出取消信号就立刻启动新 goroutine。但取消只是信号，旧 goroutine 未必已退出（可能仍卡在网关 HTTP 请求或 `enterLobby` 重试里）。后果是**新旧两个 goroutine 同时对同一账号登录**，而服务端"总是允许新登录、立即使旧会话失效"，于是两者互相顶下线，账号陷入 `登录 → 被顶 → 重登` 死循环，UI 长期显示「登录中」。

**修复（三层）**

| 层 | 位置 | 措施 |
|---|---|---|
| 1 | `startLocked` / `waitAccountExit` | `accountCancel` 增加 `done` 通道；**新 goroutine 启动后、登录前**先等前任退出 |
| 2 | 账号 goroutine 收尾 | 仅当自己仍是"现任"（`m.cancels[name] == ac`）时才 `setRunning(false, stopped)`，避免覆盖继任者状态 |
| 3 | `runAccount` | 登录成功后**先查 `ctx.Err()`** 再置在线/打日志，已取消就退出——否则这次登录会顶掉继任者的会话 |

**为何由新 goroutine 等待，而不是在 `startLocked` 里等**：`startLocked` 在持有 `m.mu` 时被 `StartAccount`/`StartAll` 调用，而旧 goroutine 的退出路径最后也要拿 `m.mu` 清理 `cancels`——持锁等待会**死锁**。放到新 goroutine 里等就没有这个问题。

等待上限 `accountExitTimeout = 30s`，超时仅告警并继续（宁可承担一次并发登录风险，也好过账号永远起不来）。

### 5.8 登录假死：enterLobby 超时收敛

`登录成功` 是在 `session.Login`（**同步包含 `enterLobby`**）返回之后才打印的。原先 `enterLobby` 重试 5 次、每次用网关客户端默认 30s 超时，最坏 `5×30+15 ≈ 165s`。网关不响应时，账号会在 UI 上"登录中"**假死近 3 分钟且日志无任何输出**，极易被误判为账号有问题。

现改为：重试 3 次、单次独立超时 8s（最坏 ≈ 27s）。512 失败并不阻断登录（`/game/` 侧任务/战队/签到不依赖它），房间挂机由既有重试策略兜底。

### 5.9 崩溃取证：存活心跳

线上曾出现「进程无声消失」：所有 goroutine 都挂了 `recover`，日志里却既无 `退出` 也无任何 panic/错误，无法判断是程序崩溃还是外部原因（系统重启、Windows 更新、被强杀）。

判据设计：**优雅退出会删除 `run_state.json`，因此文件残留即代表上次异常终止**。

- 运行中每分钟刷新 `run_state.json`（`started` / `last_alive`）
- 优雅退出（Ctrl+C / 托盘退出 / SIGTERM）时删除该文件
- 下次启动若文件仍在 → 打 `ERROR 检测到上次运行未正常退出`，附上最后存活时刻与停止时长，并写入 `startup_error.txt`

排查方法：把 `last_alive` 与 Windows 事件日志 **6006 / 6008 / 1074** 对照——紧邻即为系统关机/重启（外部原因，应配开机自启）；系统当时一直在运行才按程序崩溃排查。

> 注：Go 的 **fatal error**（如并发 map 读写）无法被 `recover()` 捕获，会直接终结进程且不留 Go 层日志，这是"无声消失"的少数几种可能之一。

### 5.10 守护进程（崩溃自动重启）

**背景**：连续两晚主程序在凌晨无声消失，所有账号挂机停止，第二天才发现。
主程序日志里既无「退出」也无 panic，无法判断崩溃还是外部原因。

**2026-09-01 现场调查结论（供后续排查参考）**

崩溃时刻锁定在 `03:10:35`，即 wingser 房间挂机「3 小时目标达成、自动退出挂机」的瞬间。
已排除的假设：

| 假设 | 结论 |
|---|---|
| 两个账号**同时**退房间导致竞争 | ❌ 不成立。chouyoku 的检查点在 `03:10:56`，比 wingser 晚 21 秒；进程 03:10:35 已死，chouyoku 根本没走到，且其日志止于 `03:10:02`（仍在挂机中） |
| Go 层 panic | ⚠️ **2026-09-02 推翻，真凶就是 panic**。但不是业务 goroutine 的 panic——它发生在 `golang.org/x/net/http2` **自己起的 goroutine** 里，本项目的 `recoverGoroutine` 保护不到；且 `-H windowsgui` 下 stderr 被丢弃，所以日志无痕。详见 5.16 |
| 日志缓冲导致最后几行丢失 | ❌ `logx` 直写 `*os.File`（O_APPEND），无缓冲 |
| 共享状态竞争 | ❌ 已核查 `w.tasks`/`w.stats`/`claimedToday`/`Mission.Extra` 均在对应锁内；`ContributeStore` 全方法加锁；`room.Hang` 状态全为局部变量；`updateStats` 复制切片 |

**残留疑点**：wingser 最后一行是 `room: status poll failed ... context canceled`（在 `room.Hang` 返回前打印），
而紧接着的 `房间挂机会话结束`（`Hang` 返回后立即打印）**从未写出**——进程死于这两者之间的极窄窗口。
该窗口内的 panic 会被 `recoverGoroutine` 捕获并记日志，既然没有记录，说明不是 panic。

**结论修正（2026-09-02，以 stderr.log 实证为准）**：

原先"指向 Go fatal error"是**推测**，现已被 5.14 加装的 stderr 重定向**直接证伪**——
`logs/stderr.log` 抓到了完整堆栈，**真凶是 `crypto/rand` 的 panic**（不是 fatal error），
详见 5.16。9/1、9/2 两次"凌晨 3 点前后崩溃"是同一原因。

> 教训：5.14 的 stderr 重定向是这次能破案的关键——**不是没有错误，而是错误曾被丢弃**。
> 这条取证手段要长期保留，不要因为"装上后没再出问题"而移除。
>
> 另注：本节的"并发 map 读写"等 fatal error 假设虽被推翻，但 5.14 的
> 运行时指标曲线（goroutines / heap 走势）仍是排查泄漏类问题的有效手段，同样保留。

已按此结论加装取证手段（详见 5.11 / 5.14 / 5.16）。

**方案**：新增独立 exe `cmd/guard`（发布为 `youjuhang-guard.exe`，与主程序同目录）。
守护进程只做「读心跳 + 拉起」，逻辑极简，崩溃概率远低于主程序。

**契约文件 `run_state.json`**（主程序写、守护进程读）

```json
{ "pid": 12345, "exe": "C:\\...\\youjuhang-win64.exe",
  "args": ["-config","accounts.yaml"], "started": "...", "last_alive": "..." }
```

| 判定 | 条件 | 动作 |
|---|---|---|
| 健康 | 心跳未过期 | 不动作 |
| 崩溃 | 心跳过期 **且** 进程已消失 | 立即重新拉起 |
| 假死 | 心跳过期但进程仍在 | **连续两次**确认后 kill 再拉起 |
| 优雅退出 | 心跳文件被删除 | 守护进程自行结束 |

**关键设计点**

- **心跳是唯一权威信号**，PID 仅用于区分"崩溃/假死"。
- **假死必须二次确认**：系统休眠唤醒后心跳会陈旧，但主程序恢复后 1 分钟内即刷新，
  第二次检查便恢复新鲜，避免误杀刚从休眠恢复的健康进程。
- **阈值保守**：默认 `stale-after=5m`（心跳周期 1m 的 5 倍），容忍休眠/IO 卡顿/日志轮转抖动。
  误杀健康进程比崩溃更糟。可用 `-check-interval` / `-stale-after` 调整。
- **单实例**：主程序每次启动都尝试拉起守护进程，靠 `guard.lock`（内含 PID，
  持有者已死则接管）保证不叠加进程。
- **拉起时追加 `-no-browser`**：服务端/后台场景不应反复弹浏览器。
- **主程序可独立运行**：找不到守护 exe 或拉起失败时仅记日志后继续，绝不启动失败。

**Windows 进程存活检测的坑（实测修正）**

`os.FindProcess` 在 Windows 上对**已退出的进程仍可能返回成功**（进程对象在句柄未全部关闭前不销毁）。
联调时表现为：主程序崩溃后守护进程仍认为其存活 → 把崩溃误判为假死 → 多等一轮确认，
还去 `Kill` 一个已死的进程（`TerminateProcess: Access is denied`）。

修正：按平台拆分（`process_windows.go` / `process_other.go`），Windows 上用
`OpenProcess` + `GetExitCodeProcess`，以退出码是否为 `STILL_ACTIVE(259)` 判定。

**重启后不重复执行已完成任务**

| 项目 | 机制 |
|---|---|
| 任务领取 | 以服务器状态为准，`f5≠0`（已领）不再重复领取 |
| 每日签到 | 服务器返回"已签/point not meet"，程序视为当日已完成 |
| 战队捐献 | `contribute_state.json` 按 uid+日期持久化，跨重启不重复捐 |

### 5.11 启动/退出顺序（守护进程生命周期）

**启动**：主程序 `startGuard()` 拉起 `youjuhang-guard.exe`（不存在则独立运行，仅记 Info）。

**退出**（2026-09-01 用户反馈后修正）：

```go
<-done              // mgr.Run 返回（Ctrl+C / 托盘退出 / SIGTERM）
stopGuard()         // ① 先主动结束守护进程（读 guard.pid → Kill）
_ = os.Remove(runStatePath())  // ② 再删除存活标记
slog.Info("youjuhang 退出")
```

为什么要 ① 在 ② 之前且必须**主动 Kill**：
原先只删除 `run_state.json`，靠守护进程自己轮询发现文件消失才退出，
期间最多有 1 个 `check-interval`（默认 15s）守护进程仍然存活，
用户会看到「主程序退出了但 guard 还在」；更糟时守护进程可能在感知到退出前
就把主程序重新拉起。

**PID 传递**：守护进程拿到单实例锁后写 `guard.pid`（含自身 PID），正常退出时删除。
主程序 `stopGuard()` 读该文件并 `Kill`，随后删除 PID 文件（双保险）。

测试：`cmd/youjuhang/stop_guard_test.go`（用测试二进制自身当「假守护进程」验证确实被杀，
以及 PID 文件缺失 / 内容非法时的静默降级）。

### 5.12 任务状态持久化（StateStore）

**为什么合并**（2026-09-01 用户要求）：原先分两个文件
`contribute_state.json` + `mall_state.json`：

1. **并发写竞争**——多账号 goroutine 同时触发写入时两个文件分别写，
   文件系统层无事务保证，可能出现一个写成功另一个失败。
2. **运维繁琐**——备份 / 迁移 / 查看都得分别处理两份文件。

**新设计**：单一 `task_state.json`，单实例互斥锁（`sync.Mutex`）保护并发写。

每次保存的顺序（`saveLocked`）：

1. 读当前主文件内容；
2. 写入 `task_state.json.bak`（损坏时可手动恢复）；
3. 写入新内容到主文件。

**加载优先级**（`load`）——四种情况：

| 序 | 情况 | 处理 |
|---|---|---|
| ① | **旧格式内容 + 新文件名**（用户把 contribute_state.json 改名过来） | 识别顶层 `date`/`by_uid`，迁入 `Contribute` 并重写 |
| ② | 新格式（`contribute`/`mall`） | 直接加载 |
| ③ | 主文件损坏 | 用 `.bak` 恢复 |
| ④ | 主文件不存在 | 从 `contribute_state.json` / `mall_state.json` 迁移并删除旧文件 |

> ① 必须最先判断：Go 的 `json.Unmarshal` 对**不匹配的顶层字段会静默忽略并返回成功**，
> 旧格式被直接解析进 `stateData` 时结果是零值却「解析成功」→ 捐献记录凭空消失 →
> `ContributeToday` 返回 0 → **重复捐献**（真实事故，已由测试覆盖）。

**结构**：

```go
type stateData struct {
    Contribute contributeData `json:"contribute"`
    Mall       mallData       `json:"mall"`
}
```

**API**（按用途命名空间）：
- 捐献：`ContributeToday` / `ContributeAdd` / `ResetContributeIfNewDay`
- 月度礼包：`MallIsClaimed` / `MallIsUnavailable` / `MallMarkClaimed` /
  `MallMarkUnavailable` / `MallLastInfo`

> 坑：`MallMarkClaimed` / `MallMarkUnavailable` 写入前必须 `ensureMallMapLocked()`。
> 新部署时 `Mall.ByUID` 是 **nil map**，直接赋值会 `panic: assignment to entry in nil map`
> 且该路径无 recover，会终结进程。

测试：`internal/biz/rollover_test.go`、新增的 `state_store_test.go`
（跨天重置、`.bak` 轮换、旧文件迁移、旧格式改名、生产文案匹配）。

### 5.13 月度礼包（每月一次的状态机）

**接口**：`internal/mall`，HTTP 明文 → `gotvgmall.gotvg.cn`。
流程：先 GET 首页拿 `PHPSESSID` cookie，再 POST `ajax_get_package`（`id=1` 微信绑定礼包）。

**语义**：每月 1-7 日可领，**每月一次**。领取后本月恒为"已完成"，下月自动重置。

**状态判定（实测样本 2026-09-01）**

| 服务器返回 | 含义 | 处理 |
|---|---|---|
| `status=1` | 首次领取成功 | 标记本月已领 |
| `status=0, info="您已经领取过此礼包"` | **本月已领过**（可能在游戏客户端领的，或上次运行领了但状态未持久化） | 标记本月已领，本月不再请求 |
| `status=0, info` 含"未绑定"/"绑定微信" | 硬性不可领 | 标记本月不可领，本月不再请求 |
| 其他 | 未知失败 | 记录告警，下轮可重试 |

**为什么必须持久化**（2026-09-01 用户反馈"3 个账号都显示未领取"）：

原先"本月是否已领"只存在 Worker 的**内存字段** `mallOKKey` 里，进程一重启就归零：

1. 已领过的账号重启后 UI 退回"未领取"，误导用户；
2. 1-7 号期间每天重复请求领取接口，产生无效请求。

修复：新增 `MallStore`（`internal/biz/mall_state.go`），按 **uid + 月份 key** 持久化到
`mall_state.json`，与 `ContributeStore` 同样由 `Manager` 持有**全局唯一实例**（多账号共享，
否则并发写会相互覆盖）。

判据优先取持久化状态（`IsClaimed`/`IsUnavailable`），内存字段仅作为补充，
因此"在游戏客户端领过"也能正确显示为已完成。

> 待精确化：`mallUnavailable()` 目前按"未绑定/绑定+微信"关键词匹配，**尚无实测样本**。
> 若出现未绑定微信的账号，其 `info` 原文会写入 `mall_state.json` 的 `last_info` 字段，
> 据此即可精确匹配。

### 5.14 崩溃取证：stderr 重定向 + 运行时指标

**根因认知**：`-H windowsgui` 无控制台，标准错误被丢弃；而 Go 的 **fatal error**
（并发 map 读写、OOM、栈溢出）不可被 `recover()` 捕获，运行时把错误与完整
goroutine 调用栈写到 **fd 2** 后终止进程 → 「进程无声消失、日志无痕」。

**① 标准错误重定向**（`cmd/youjuhang/stderr_windows.go` / `stderr_other.go`）

启动时把**进程级**标准错误句柄指向 `logs/stderr.log`：

- Windows：`SetStdHandle(STD_ERROR_HANDLE, f.Fd())`
- Unix：`syscall.Dup2(int(f.Fd()), 2)`
- 必须在 `main()` **最开头**调用，且**全程持有**文件句柄（存于 `keepStderrFile`，
  否则 `*os.File` 被 GC 回收时 close 底层句柄，崩溃时就写不进去了）

**实测验证**：windowsgui 模式下先 `SetStdHandle` 再触发并发 map 读写，
`fatal error: concurrent map read and map write` 连同全部 goroutine 堆栈完整落入文件。

> 关键细节：`os.Stderr` 这个变量在包初始化时就绑定了**原始**句柄，重定向**不影响**它；
> 而 Go 运行时是在崩溃**当场**调用 `GetStdHandle` 取句柄，所以恰好能截获。
> 这也意味着不能靠"往 os.Stderr 写点东西验证"来测试本机制（那样会写回原句柄）。

守护进程拉起主程序时也会用 `cmd.Stdout/cmd.Stderr` 指向同一文件作为双保险。

**② 运行时指标**（`core.metricsLogger`，每 30 分钟一条 + 启动基线）

输出 `goroutines` / `heap_alloc_mb` / `heap_sys_mb` / `sys_mb` / `num_gc` / `uptime_min`。

崩溃后回看曲线即可定性：

| 现象 | 指向 |
|---|---|
| `goroutines` 持续爬升 | goroutine 泄漏（循环里起新 goroutine 且旧的不退出） |
| `heap_alloc_mb` 单调增长不回落 | 内存泄漏，最终 OOM |
| 两者均平稳 | 排除泄漏，转查并发 map 等 fatal error |

### 5.15 状态推送

`Worker.notifyStats()` → `StatsObserver` 回调 → `core` 更新状态 → 前端轮询 `/api/status` 展示。

**踩坑**：observer 里无条件 `setStatus(online)`，导致点「停止」后，Worker 退出路径上的最后几次 `notifyStats` 把状态刷回「在线」。修复：observer 中检查 `ctx.Err() == nil`，停止后不再覆盖状态。

---

### 5.16 真凶：crypto/rand 的 ProcessPrng panic（2026-09-02 破案）

**现象**：主程序反复"无声消失"，由 guard 拉起；用户看到右下角出现两个托盘图标。
`youjuhang.log` 里既无「退出」也无任何错误，`startup_error.txt` 只报"上次异常终止"。

**根因**（`logs/stderr.log` 抓到的完整堆栈）：

```
panic: Failed to find ProcessPrng procedure in bcryptprimitives.dll
golang.org/x/net/http2.(*ClientConn).writeStreamReset
crypto/rand.Read → internal/syscall/windows.ProcessPrng → mustFind panic
```

- Go（**含 1.21**）的 `crypto/rand` 在 Windows 上直接调用 `bcryptprimitives!ProcessPrng`；
- **Win7 / 旧 Windows Server** 的 `bcryptprimitives.dll` **不导出** `ProcessPrng`；
- 缺失时 `LazyProc.Addr()` → `mustFind()` 直接 **panic**；该 goroutine 由 `x/net/http2`
  自己创建，项目的 `recoverGoroutine` **保护不到** → 进程当场终结。

**为什么能稳定运行数小时才崩**：gate 用**明文 HTTP/2**（h2c，无 TLS），
稳态下 `crypto/rand` 根本不被调用；只有发生 **HTTP/2 流重置**（连接抖动 / 服务端断流）
时 `writeStreamReset` 才调 `crypto/rand.Read`，一触即崩。
故表现为"随机时刻崩溃"——19:45 那次撑了 7 小时，到 02:59 撞上流重置才死。

**关键反直觉点：把 bcryptprimitives.dll 拷到程序目录是无效的。**

用户按旧结论拷贝了 DLL，崩溃依旧。原因在 Go 的 **sysdll 机制**：

```go
// internal/syscall/windows/zsyscall_windows.go
modbcryptprimitives = syscall.NewLazyDLL(sysdll.Add("bcryptprimitives.dll"))
```

`sysdll.Add` 把它登记进 `sysdll.IsSystemDLL`，`syscall/dll_windows.go` 随即改走
`loadsystemlibrary`（而非普通 `loadlibrary`），后者等价于：

```
LoadLibraryEx(name, 0, LOAD_LIBRARY_SEARCH_SYSTEM32)   // 只搜 System32
```

**exe 目录与 PATH 一概被忽略**（设计目的正是防 DLL 劫持，Go issue 14959）。
因此无论程序目录放什么版本，加载到的永远是 System32 里那份缺导出的 DLL——
与拷贝的 DLL 位数、来源系统都无关。

**修复**（`cmd/youjuhang/rng_windows.go`）：

在 `main.init` 阶段把 `crypto/rand.Reader` 换成 **`advapi32!RtlGenRandom`**
（导出名 `SystemFunction036`）：

- `crypto/rand.Read` 最终委托给 `crypto/rand.Reader`（可覆盖的导出变量），
  替换后 HTTP/2 等所有调用改走 `RtlGenRandom`，**完全绕开 ProcessPrng**；
- `RtlGenRandom` 自 **Windows XP** 起就存在于 advapi32.dll，Win7 必然可用；
- 我们的 `NewLazyDLL("advapi32.dll")` **不经 `sysdll.Add`**，走常规搜索路径，不受 System32 限制；
- 这正是 Go **runtime** 在 `ProcessPrng` 缺失时用的同一套回退（`runtime.getRandomData`），
  只是 `crypto/rand` 包自己没做，我们在应用层补上。

> 注：换 Go 版本**解决不了**（1.21 与 1.24 都走 `ProcessPrng`）；拷贝 DLL 也解决不了（见上）。
> 应用层替换 `Reader` 是唯一干净的方案。

**现状与判读**：部署后启动日志会打印 WARN
`bcryptprimitives.dll 缺失 ProcessPrng 导出…已自动回退到 RtlGenRandom 兜底`——
这是**正常且预期**的，不代表故障。可用 `-check-rng` 单独验证（结论写入 `rng_check.txt`）。

---

### 5.17 经验头像（保持最高经验加成，2026-09-09 新增）

个人空间的部分头像带**平台经验加成**（`additional_exp`，实测 1.1 / 1.2 / 1.4 等档位），
但都是**限时**的（页面显示「剩余N天」，30 天一续），到期加成即失效。
多账号手动维护成本高，故由程序每天检查、**必要时**才换戴。

- **实现位置**：协议 `internal/mall/dress.go`（`GetSpace` / `ListDress` / `DressUp`），
  业务 `internal/biz/avatar.go`，配置 `avatar_enabled`（默认开）。
  协议细节见 [avatar_api.md](avatar_api.md)。
- **执行时机**：`Worker.Run` 首轮 + 每轮主循环调用 `startAvatarCheck`，
  内部按 `avatarKey`（日期）去重，跨天由 `rolloverIfNeeded` 清零 → **每天一次**。
- **异步执行**（2026-09-10）：`startAvatarCheck` 起独立 goroutine（挂 `recoverGoroutine`），
  **不阻塞主循环**。原因：检查要访问商城（space 页 + 可能翻页拉全量列表），
  实测 3~4 秒（wingser 15:27:09→15:27:12，count=49），同步执行会推迟战队捐献、
  房间挂机启动与状态推送，表现为「登录后账号数据加载很慢」。
  头像结果出来后由该 goroutine 单独推一次状态，UI 头像行随后填充（其余数据早已显示）。
- **异步后的两项状态一致性防护**（2026-09-10 安全审查，均有单测锁定）：
  1. **`avatarChecking` 并发标志**：跨天时 `rolloverIfNeeded` 会清零 `avatarKey`，
     若上一轮 goroutine 仍在跑（慢网络），主循环会误判"今天没查过"再起一个，
     两个 goroutine 并发佩戴 → 状态抖动。启动条件改为
     `!avatarChecking && avatarKey != 今天`，两者在 `statsMu` 下**原子判断并置位**；
     goroutine 结束（含 panic 路径）时清标志，避免永久卡死。
  2. **`WornURL` 为空即放弃决策**：space 页异常（限流页 / 未登录跳转 /
     解析不到 `userAvatar`）时 `WornURL` 为空，此时若照常"选最优佩戴"，
     等于在**不知道用户当前戴了什么**的情况下覆盖它（可能换掉用户手动选的头像）。
     故拿不到当前佩戴就**什么都不做**——每天都有机会，不必急于这一次。
- **决策以服务端查询为唯一事实**（2026-09-09 按用户需求重写）：
  先 `GET /?token=..&userid=..&space=1`（个人空间初始化页，一个请求同时返回
  「当前佩戴」`userAvatar` 与「拥有列表」），按 URL 匹配出当前佩戴的条目：
  - 加成 `>1` 且未过期 → **什么都不做**（不翻页、不佩戴）；重启/重登也不触发佩戴。
  - 加成 `=1`（无加成）/ 未佩戴 / 已过期 → 翻页拉全量列表，`PickBestDress` 选最优佩戴。
- **选择策略**（`mall.PickBestDress`）：在加成 > 1 且未过期的头像中，加成高的优先；
  加成相同取剩余天数多的（减少切换频率）。
- **常态开销**：佩戴有效时每账号每天只有 **1 个 GET 请求**（space 页）；
  只有需要更换时才翻页。
- **失败处理**：加成型头像多绑定页游角色（如「刀剑笑之霸刀 7000 级」），
  账号在该页游无角色/等级不足时佩戴失败（响应 `num != 2`）。只记日志与 UI 状态、
  **不重试**（重试无意义），也不影响挂机主流程。

**两个必须记住的坑**（详见 avatar_api.md 第四节）：

1. `end_time` 字段**不是**当前到期时间，而是上次购买/续费的周期起点，
   真实到期 ≈ `end_time` + 30 天。判断过期**必须**读页面「剩余N天」文案，
   用 `end_time` 会把还剩 24 天的头像误判为已过期并反复切换。
2. 「剩余N天」位于 `good_item` **内部末尾**的 `cont` 块中，
   必须按 `<div class="good_item">` 起点切块解析；若按 `good_id` 位置切块会串到下一条目
   （58 的 24 天被读成 125 的 9 天）。`dress_test.go` 用真实抓包结构锁定了该行为。

> UI（2026-09-09 调整）：佩戴头像**不是任务**，不进任务徽章；状态随
> `UserStats.Avatar`（`biz.AvatarInfo`）推送，由前端渲染在「基础信息」列：
> 已佩戴显示 `头像 铁血士兵 1.4倍·剩24天`（绿色），未佩戴灰显原因（未启用/无可用/失败原因）。

---

### 5.18 Web UI 假死：XHR 无超时占满浏览器连接池（2026-09-10）

**现象**：控制台打开一段时间后点击「刷新」无反应，必须整页刷新（F5）才恢复。
用户反馈自 1.1（头像功能）后开始出现。

**根因**：前端 `api()` 只处理 `readyState===4`，**没有 `timeout` / `onerror` / `ontimeout`**。
请求一旦挂起（网络抖动、后端偶发慢），回调永不触发、连接一直占用。
浏览器对同一域只有 **6 个并发连接**（HTTP/1.1），挂起请求累积几个即占满连接池，
之后所有请求（含点击「刷新」）全部排队 → 表现为 UI 假死。
**整页刷新会中止挂起请求并释放连接，所以 F5 就能恢复**——这是判断该问题的关键特征。

自动刷新 10 分钟一次，故长时间运行后必然爆发。与头像功能无因果关系，属既有缺陷，
1.1 后运行时间更长才暴露（不要被"刚好在新版本后出现"误导）。

**排查过程（同类问题可复用）**：

1. 后端健康：主日志 goroutines 稳定（13~17）、无 panic → 排除崩溃/死锁；
2. `/api/status` 只做 `Snapshot()`（短暂持锁），主循环不持 `m.mu` → 排除被业务阻塞；
3. `tailLines` 是倒序分块读（≤1MB）且前端未调用 `/api/logs` → 排除 64MB 日志拖慢；
4. 前端 `api()` 无超时与错误回调 → **定位**。

**修复**：

- `api()` 加 `xhr.timeout = 15s` 与 `ontimeout / onerror / onabort`，保证任何情况都有回调；
- `done` 标志保证回调只触发一次（超时与响应可能竞争）；
- `loadStatus()` 加防重入 `statusLoading`，上一次未返回前不再发新请求。

---

## 六、构建

### 6.1 工具链

| 工具 | 路径 |
|---|---|
| Go 1.21.13（默认） | `D:\Git\tools\go-sdk\go121\bin\go.exe` |
| Go 1.24.3（备用） | `D:\Git\tools\go-sdk\go\bin\go.exe` |
| rsrc（manifest/图标） | `%USERPROFILE%\go\bin\rsrc.exe` |
| tshark | `D:\Program Files\Wireshark\tshark.exe` |

**必须使用 Go 1.21**：Go 1.22+ 编译产物可能无法在 Windows 7 启动。

### 6.2 构建命令

```bat
cd server
build.bat
```

产出：

- `dist/youjuhang-win64.exe`（amd64）
- `dist/youjuhang-win32.exe`（386）
- 自动复制 `configs/accounts.yaml` 到 `dist/`

关键 ldflags：`-s -w` 裁剪符号，`-H windowsgui` 隐藏控制台窗口。

手动构建：

```bash
set CGO_ENABLED=0
set GOOS=windows
set GOARCH=amd64
go build -trimpath -ldflags "-s -w -H windowsgui" -o dist/youjuhang-win64.exe ./cmd/youjuhang
```

### 6.3 测试

```bash
go test ./...
```

| 测试文件 | 覆盖内容 |
|---|---|
| `internal/biz/rollover_test.go` | 跨天重置逻辑 |
| `internal/core/core_test.go` | 侦查账号随机挑选（排除/轮换/分布）、兜底账号挑选（优先级/排除/停用过滤） |
| `internal/config/config_test.go` | 挂机有效期计算（跨月/跨年/闰年）、到期判定、续期增减规则 |

### 6.4 入库范围（.gitignore）

仓库只收录**源码 + 文档 + 分析脚本**，其余由根 `.gitignore` 排除：

| 排除项 | 原因 |
|---|---|
| `captures/` | 约 **12.8GB** / 306 个 pcapng。远超 GitHub 单文件 100MB 硬限，且含真实会话数据。本地保留作取证素材，文档中引用的 pcap 文件名仅作留档 |
| `server/dist/` | 构建产物约 41MB，且含**真实明文密码**的 `accounts.yaml` |
| `accounts.yaml` | 兜底：排除仓库中任何位置的账号配置（真实配置含明文密码）。仅 `server/configs/accounts.yaml` 例外——它已脱敏为占位值，作为模板入库 |
| `server/cmd/youjuhang/rsrc.syso` | rsrc 每次构建自动生成（图标 + manifest 二进制资源） |
| `logs/` `*.log` | 运行日志，含账号昵称 / UID 等信息 |
| `tools/tool_paths.ini` | 含本机绝对路径与 Windows 用户名。脚本有硬编码回退，忽略后不影响使用；模板见 `tools/tool_paths.example.ini` |
| `run_state.json` `guard.lock` `guard.pid` `task_state.json` `*.bak` `startup_error.txt` `rng_check.txt` | 程序自动维护的运行时状态 |
| `__pycache__/` `*.pyc` | Python 字节码缓存 |
| `.playwright-cli/` `.codebuddy/plans/` `.idea/` `.vscode/` | 工具与编辑器临时产物 |

**推送前自检**（确认没有隐私数据被暂存）：

```bash
# 期望：仅有 server/configs/accounts.yaml（脱敏样例）一行
git ls-files | findstr /i "accounts.yaml"

# 期望：无任何输出
git status --short | findstr /i "\.pcapng \.exe dist/"
```

> 若曾误提交过含密码的配置，**历史中会一直保留**——需改写历史
> （`git filter-repo` 或 BFG）后再公开；单纯删除文件并提交无效。

---

## 七、逆向工具链

抓包与协议分析方法论沉淀在 `.codebuddy/skills/protocol-capture-reverse/`：

| 文件 | 内容 |
|---|---|
| `SKILL.md` | 抓包 → 解析 → 对照实验 → 固化 的完整工作流 |
| `references/protocol_map.md` | 消息 ID、字段结构、通道与封帧 |
| `references/error_codes.md` | 返回码含义，哪些算失败哪些算正常 |
| `references/case_studies.md` | 7 个真实排查案例 |
| `references/tooling.md` | 环境路径、tshark 字段、抓包资产清单 |
| `scripts/` | tshark 抓包控制、HTTP/2 流解析、protobuf 解码、Frida hook |

**核心方法：对照实验。** 抓包只能告诉你客户端怎么做，不能告诉你哪一步是必需的。正确做法是一次只改一个变量，用探针程序直接对服务器发消息验证。

本项目靠这个方法定位出「漏发 512 导致所有房间消息 `-10 WRONG_GAME_TOKEN`」——纯读代码和看抓包都没找出来。

**重要原则**：否定性结论（"这是加密的"/"做不到"）代价极高，必须亲手验证。本项目曾因注释断言「1025 响应是强加密数据」而长期随机盲猜房间号，实际明文且能解析出 187 个房间。

---

## 八、文档索引

| 文档 | 内容 |
|---|---|
| [README](../README.md) | 功能说明与使用（面向使用者） |
| **本文** | 架构、模块、协议、构建、开发（面向维护者） |
| [protocol_notes.md](protocol_notes.md) | 协议笔记：通信架构、封帧、消息 ID、登录与业务流程 |
| [zone_hang_fix.md](zone_hang_fix.md) | 战盟任务无进度的诊断与修复（1042 上报的发现过程） |
| [traffic_analysis.md](traffic_analysis.md) | 流量特征分析 |
| [mall_api.md](mall_api.md) | 商城礼包接口 |
| [avatar_api.md](avatar_api.md) | 头像装扮（经验加成）接口与解析陷阱 |

`docs/` 下的 `*_strings.txt`、`sock_call_sites.txt` 是早期对客户端做静态分析时提取的字符串与调用点原始素材，保留作取证参考。

逆向方法论与工具见 `.codebuddy/skills/protocol-capture-reverse/`。

---

## 九、待办

1. **联盟自动化**：联盟任务、自动贡献、商店兑换。
2. **当日经验跨日重置规则**确认与统计修正。

> 原「全账号掉线兜底」已于 2026-08-31 实现（见 4.1 的②兜底探测），从待办移出。

---

## 十、维护建议

- **新增协议消息**：先在 `msgid.go` 加常量与构造器，再用探针程序验证，最后写进 `protocol_map.md`，注明抓包证据。
- **改判定逻辑前先抓包确认**：服务器对"无数据"与"失败"的返回结构可能不同，不要想当然。
- **子 goroutine 必须挂 recover**，尤其是会长期运行的。
- **旧的否定性结论要主动更新**，否则会持续误导后续维护。
