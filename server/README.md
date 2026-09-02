# 游聚挂机程序（源码目录）

> 功能说明与使用方法见仓库根目录 [README](../README.md)。
> 架构、协议、开发指南见 [技术文档](../docs/technical.md)。

## 构建

```bat
build.bat
```

使用 **Go 1.21** 工具链（Go 1.22+ 产物可能无法在 Windows 7 启动），产出：

- `dist/youjuhang-win64.exe`（amd64，主程序）
- `dist/youjuhang-win32.exe`（386，主程序）
- `dist/youjuhang-guard.exe`（amd64，守护进程；仅发 64 位）
- 仅当 `dist/accounts.yaml` 不存在时，才用 `configs/accounts.yaml` 样例生成一份；已存在则保留（不会覆盖真实账号配置）

守护进程是**可选组件**：主程序在启动成功后尝试拉起它，目录里没有就独立运行；
32 位系统跑不了 64 位守护进程时也仅记一条警告，不影响主程序。

## 运行

```bat
cd dist
youjuhang-win64.exe [-config accounts.yaml] [-web 127.0.0.1:29090] [-console] [-no-browser]
```

| 参数 | 说明 |
|---|---|
| `-config` | 指定配置文件路径 |
| `-web` | 控制台地址（优先级高于配置文件） |
| `-console` | 日志输出到控制台（调试用） |
| `-no-browser` | 启动时不自动打开浏览器 |

启动后自动打开 Web 控制台 `http://127.0.0.1:29090`（加 `-no-browser` 则不打开）。

守护进程由主程序自动拉起，无需手动执行；如需单独调试：

```bat
youjuhang-guard.exe -state run_state.json [-check-interval 1m] [-stale-after 5m]
```

## 配置

`configs/accounts.yaml`：

```yaml
login_addr: gamelogin3.gotvg.com:18000   # 登录服务器
web_addr: 127.0.0.1:29090                # 控制台地址（仅本机，勿改 0.0.0.0）
refresh_minutes: 10                      # 在线刷新间隔（分钟，默认 10，带随机抖动）

check_in_enabled: true      # 每日签到
auto_claim: true            # 自动领取任务奖励
mall_enabled: true          # 商城礼包（每月 1-7 日）
room_hang_enabled: true     # 战盟房间挂机（仅已加入战队的账号生效）
contribute_enabled: true    # 战队捐献
contribute_daily: 3000      # 每日捐献目标
contribute_min_balance: 3000  # 余额下限（低于此值不捐）
contribute_step: 1000       # 单次捐献量（须为 100 的倍数，程序会自动向下规整）

accounts:
  - name: "chouyoku"
    password: "xxxxx"
    enabled: true
    # mac:        可选，省略则首次启动自动生成并写回（多账号各自独立）
    # expire_date: "2026-09-03"  # 可选，挂机到期日；留空=长期挂机
```

完整配置项见 `internal/config/config.go`。

> 日常使用无需手写 `mac` / `expire_date`，在 Web 控制台新增或编辑账号即可，
> 到期日按填写的天数自动计算（编辑时填的是**在原到期日上增减的天数**，可为负）。

## 日志

```
logs/youjuhang.log    主日志
logs/<账号名>.log     每账号独立日志
logs/guard.log        守护进程日志（崩溃检测与自动拉起记录）
```

运行目录下的状态文件（均为 JSON，按 uid 持久化，跨重启保留）：

```
task_state.json        任务状态（2026-09-01 合并：当日捐献量 + 月度礼包领取状态）
task_state.json.bak    上次写入的备份（损坏时可手动恢复；首份有效写入后才有）
run_state.json         存活心跳（守护进程用；优雅退出时删除）
guard.pid              守护进程 PID（主程序退出时据此主动结束它）
```

旧文件 `contribute_state.json` / `mall_state.json` 会在启动时**自动迁移**到 `task_state.json` 并删除。

> 兼容提示：若你手动把 `contribute_state.json` 改名成 `task_state.json`，
> 程序会识别到"旧格式内容 + 新文件名"并自动迁移，**不会**丢失捐献记录。

### 启动/退出顺序

- **启动**：主程序启动成功后拉起 `youjuhang-guard.exe`（不存在则独立运行）。
- **退出**：主程序先**主动结束守护进程**，再删除 `run_state.json`，最后自己退出。
  这样不会出现"主程序已退出、guard 还活着"或"退出后又被拉起"的情况。

守护进程源码未变化时，`build.bat` 会**自动复用已有的 `youjuhang-guard.exe`**，不重复编译。
如需强制重编，删除 `dist\youjuhang-guard.exe` 后重新运行 `build.bat` 即可。

主程序还会维护 `run_state.json`（存活心跳，优雅退出时删除）：
下次启动若发现残留，会在日志与 `startup_error.txt` 报告上次异常终止的最后存活时刻，
用于区分「程序崩溃」与「系统重启」（对照 Windows 事件日志 6006/6008/1074）。

### 崩溃取证：stderr 重定向与运行时指标

程序用 `-H windowsgui` 构建，**没有控制台，标准错误输出会被直接丢弃**。
而 Go 的 **fatal error**（并发 map 读写、OOM、栈溢出等）无法被 `recover()` 捕获，
运行时会把错误与完整 goroutine 调用栈写到 **fd 2** 后终止进程——
表现就是「进程无声消失、日志里什么都没有」。

为此做了两件事：

1. **标准错误重定向**（`cmd/youjuhang/stderr_windows.go`）
   启动时用 `SetStdHandle(STD_ERROR_HANDLE, ...)` 把进程级标准错误指向 `logs/stderr.log`。
   实测确认：windowsgui 模式下触发并发 map 读写，
   `fatal error: concurrent map read and map write` 及全部堆栈均完整落盘。
   > 注意：`os.Stderr` 变量在包初始化时就绑定了原始句柄，重定向**不影响**它；
   > 而运行时是在崩溃当场调用 `GetStdHandle`，所以恰好能截获——这正是我们要的。

2. **运行时指标**（`core.metricsLogger`，每 30 分钟一条）
   输出 `goroutines` / `heap_alloc_mb` / `heap_sys_mb` / `sys_mb` / `num_gc` / `uptime_min`。
   用于判定崩溃是否源于资源泄漏：

   | 现象 | 指向 |
   |---|---|
   | `goroutines` 持续爬升 | goroutine 泄漏 |
   | `heap_alloc_mb` 持续爬升不回落 | 内存泄漏，最终 OOM |
   | 两者都平稳 | 排除泄漏，转查 fatal error（并发 map 等） |

## 目录

```
cmd/
├── youjuhang/        主入口（Web 控制台 + 托盘 + 多账号挂机）
├── guard/            守护进程：监视存活心跳，崩溃/假死自动拉起主程序
├── allinone/         调试：单账号全流程
├── diagwin7/         Win7 兼容性诊断
└── hello/            最小连通性测试

internal/
├── biz/              业务层（主循环、任务、签到、战队、捐献）
├── core/             多账号管理器
├── session/          登录状态机
├── gate/             HTTP/2 客户端
├── room/             分区/房间挂机
├── mall/             商城礼包
├── web/              Web 控制台
├── tray/             系统托盘
├── config/           YAML 配置
├── protocol/         消息 ID 与构造器
├── proto/            极简 protobuf
└── logx/             按账号日志
```

## 兼容性

- Go 1.21 工具链构建，产物可在 Windows 7 运行
- 控制台前端用 ES5 + XMLHttpRequest，兼容 Win7 下 Chrome 109 / Firefox ESR / IE11
