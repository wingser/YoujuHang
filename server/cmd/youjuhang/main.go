// youjuhang 是游聚平台在线挂机程序（多账号 + Web 控制台）
// 用法: youjuhang -config configs/accounts.yaml [-web 127.0.0.1:8080]
// 兼容 Windows 7+（使用 Go 1.21 工具链编译）
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"youjuhang/internal/config"
	"youjuhang/internal/core"
	"youjuhang/internal/tray"
	"youjuhang/internal/web"
)

func main() {
	// 必须最先执行：把进程级标准错误重定向到 logs/stderr.log。
	// 否则 windowsgui 模式下 Go 的 fatal error（并发 map/OOM，recover 抓不到）
	// 会连同调用栈一起被丢弃——这正是连续两晚「进程无声消失」无法定位的原因。
	redirectStderr(filepath.Join(logDir(), "stderr.log"))
	// 在任何 HTTP/2 调用之前探测 crypto/rand 的底层依赖。Win7/部分 Windows Server
	// 的 bcryptprimitives.dll 不导出 ProcessPrng，crypto/rand.Read 会 panic；
	// 但 rng_windows.go 的 init 已把 Reader 换成 RtlGenRandom 兜底，不会再崩。
	// 这里只做探测与分级提示（第一个返回值 false 才代表随机源确实不可用）。
	// 结果保留给下面的 -check-rng 分支复用，避免重复探测、重复打日志。
	rngOK, rngFallback := checkWindowsRNG()
	defer recoverPanic()
	// 清空上次启动的跟踪记录，重新开始分阶段留痕。
	// 只要进程开始执行就会创建此文件，用于判断程序被拦截/崩溃时执行到了哪一步。
	_ = os.Remove(filepath.Join(exeDir(), "startup_error.txt"))
	trace("program start")
	cfgPath := flag.String("config", "accounts.yaml", "配置文件路径")
	webFlag := flag.String("web", "", "Web 控制台监听地址（覆盖配置文件 web_addr；默认 127.0.0.1:29090 仅本机可访问）")
	noBrowser := flag.Bool("no-browser", false, "启动时不自动打开浏览器")
	console := flag.Bool("console", false, "日志输出到控制台（调试用），默认写入 logs/youjuhang.log")
	checkRNG := flag.Bool("check-rng", false, "只检查 crypto/rand 依赖（ProcessPrng 可用性 + RtlGenRandom 兜底）后退出；不启动挂机")
	flag.Parse()

	if *checkRNG {
		// 诊断模式：仅探测随机源并把结论写盘，不启动任何后台逻辑。
		// 便于在不出问题的情况下快速确认环境是否兼容。
		// 复用 main 开头那次探测的结果，不再重复调用 checkWindowsRNG()。
		// 注意：ProcessPrng 缺失不代表不可用——只要 RtlGenRandom 兜底能工作即可正常运行。
		result := "crypto/rand 可用：bcryptprimitives!ProcessPrng 可用"
		switch {
		case !rngOK:
			result = "crypto/rand 不可用：ProcessPrng 缺失且 RtlGenRandom 兜底失败"
		case rngFallback:
			result = "crypto/rand 可用：已回退到 RtlGenRandom 兜底（System32 的 bcryptprimitives.dll 无 ProcessPrng 导出，属正常情况，无需处理；拷贝到程序目录无效）"
		}

		resultPath := filepath.Join(exeDir(), "rng_check.txt")

		_ = os.WriteFile(resultPath, []byte(result), 0o644)

		slog.Info("crypto/rand 依赖检查完成", "result_file", resultPath, "usable", rngOK)
		return
	}
	trace("flag parsed")

	logFile := setupLogger(*console)
	if logFile != nil {
		defer logFile.Close()
	}
	trace("logger ready")

	// 必须在日志就绪后、业务逻辑前检查：上次是否异常终止。
	// 结果同时写入日志与 startup_error.txt，保证下次排查有据可依。
	reportAbnormalExit()

	cfg, err := config.Load(resolveConfigPath(*cfgPath))
	if err != nil {
		slog.Error("配置加载失败", "path", *cfgPath, "err", err)
		showStartupError("配置加载失败：" + err.Error())
		os.Exit(1)
	}
	trace("config loaded")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mgr := core.NewManager(*cfgPath, cfg)

	webAddr := resolveWebAddr(*webFlag, cfg.WebAddr)
	if !isLoopbackAddr(webAddr) {
		slog.Warn("Web 控制台将监听非本机地址，控制台可能被外部网络访问，请确认这是预期行为",
			"addr", webAddr)
	}
	srv := web.New(webAddr, mgr)
	if err := srv.Start(); err != nil {
		slog.Error("Web 控制台启动失败", "addr", webAddr, "err", err)
		showStartupError("Web 控制台启动失败：" + err.Error())
		os.Exit(1)
	}
	url := "http://" + srv.Addr()
	slog.Info("Web 控制台已启动", "url", url)
	trace("web started: " + url)

	// 系统托盘（仅 Windows；Win7 兼容），失败时降级为纯控制台模式
	var tr *tray.Tray
	if runtime.GOOS == "windows" {
		trace("tray.New begin")
		if tr, err = tray.New(mgr, url, stop); err != nil {
			slog.Warn("系统托盘初始化失败，已降级为纯控制台模式", "err", err)
			tr = nil
		}
		trace("tray.New done")
	}

	if !*noBrowser {
		openBrowser(url)
	}
	trace("browser opened")

	slog.Info("youjuhang 启动", "accounts", len(cfg.Accounts), "web", srv.Addr())

	// 存活心跳：每分钟刷新一次。既是崩溃取证依据，也是守护程序的监视信号。
	// 必须在拉起守护程序**之前**写好，否则守护程序首次检查读不到文件。
	startLivenessBeat(ctx, time.Now())

	// 拉起守护程序（可选；缺失时自动以独立模式运行）
	startGuard()

	// 挂机逻辑在后台运行
	done := make(chan struct{})
	go func() {
		defer func() {
			// 必须兜底：main() 的 defer recoverPanic() 只覆盖主 goroutine，
			// 本 goroutine 的 panic 会直接终结进程；又因 -H windowsgui 无控制台、
			// stderr 被丢弃，日志里不留痕迹（2026-08-31 崩溃事故的表现形式）。
			// 这里捕获后落盘 startup_error.txt，保证任何崩溃都留有可诊断的线索。
			if r := recover(); r != nil {
				buf := make([]byte, 16384)
				n := runtime.Stack(buf, false)
				msg := fmt.Sprintf("后台管理 goroutine panic：\n%v\n\n调用栈：\n%s", r, buf[:n])
				slog.Error("后台管理 goroutine panic，程序即将退出", "panic", r, "stack", string(buf[:n]))
				writeStartupError(msg)
			}
			close(done)
		}()
		mgr.Run(ctx)
	}()

	// 收到退出信号（Ctrl+C）时，同步结束托盘消息循环
	go func() {
		defer func() {
			if r := recover(); r != nil {
				buf := make([]byte, 8192)
				n := runtime.Stack(buf, false)
				slog.Error("托盘退出 goroutine panic", "panic", r, "stack", string(buf[:n]))
			}
		}()
		<-ctx.Done()
		if tr != nil {
			tr.Exit()
		}
	}()

	if tr != nil {
		tr.Run()
		tr.Shutdown()
	}
	<-done
	// 走到这里说明是**优雅退出**（Ctrl+C / 托盘退出 / SIGTERM）。
	// 顺序很重要：
	//   1. 先主动结束守护进程——否则它要等下一次轮询（15s）才发现主程序已退出，
	//      期间会残留存活进程，甚至可能在感知到退出前把主程序重新拉起。
	//   2. 再删除存活标记，下次启动便不会误报"异常终止"。
	stopGuard()
	_ = os.Remove(runStatePath())
	slog.Info("youjuhang 退出")
}

// runStateName 存活标记文件名（与 startup_error.txt 同目录，便于一并收集）
const runStateName = "run_state.json"

// guardExeName 守护程序文件名（与主程序同目录；不存在时主程序独立运行）。
// Windows 需要 .exe 后缀，Unix 不需要，按平台返回。
func guardExeName() string {
	if runtime.GOOS == "windows" {
		return "youjuhang-guard.exe"
	}
	return "youjuhang-guard"
}

// runState 存活标记内容（同时是守护程序的监视/拉起依据）
type runState struct {
	PID       int      `json:"pid"`        // 主进程 PID，供守护程序判断存活
	Exe       string   `json:"exe"`        // 主程序绝对路径，供守护程序重新拉起
	Args      []string `json:"args"`       // 主程序启动参数，原样用于拉起
	Started   string   `json:"started"`    // 本次启动时间
	LastAlive string   `json:"last_alive"` // 最后存活时刻（每分钟刷新）
}

// guardPIDFileName 守护进程 PID 文件名（与 cmd/guard 保持一致）
const guardPIDFileName = "guard.pid"

// stopGuard 主动结束守护进程。
//
// 为什么需要（2026-09-01 用户反馈）：主程序退出时若只是删除 run_state.json，
// 守护进程要等下一次轮询（默认 15s）才发现"文件消失"才退出，
// 期间守护进程仍然存活，用户会看到"主程序退出了但 guard 还在"。
// 更糟的情况：守护进程可能在感知到退出之前就把主程序重新拉起。
//
// 因此主程序在自身退出流程中**先**主动结束守护进程，再清理自己的存活标记。
func stopGuard() {
	pidFile := filepath.Join(exeDir(), guardPIDFileName)
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return // 没有 PID 文件：守护进程未运行或已退出，无需处理
	}
	var pid int
	if _, err := fmt.Sscan(strings.TrimSpace(string(raw)), &pid); err != nil || pid <= 0 {
		_ = os.Remove(pidFile)
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		_ = os.Remove(pidFile)
		return
	}
	if err := p.Kill(); err != nil {
		// 结束失败（如进程已退出）不阻塞主程序退出，仅记录。
		slog.Warn("结束守护进程失败", "pid", pid, "err", err)
	} else {
		slog.Info("守护进程已结束", "pid", pid)
	}
	_ = os.Remove(pidFile)
}

// startGuard 拉起守护程序（若发布目录中存在）。
//
// 设计约束：主程序必须能**独立运行**——守护程序只是可选的存活增强组件。
// 目录里没有它时仅记一条 Info 后正常继续，绝不因缺少守护程序而启动失败。
// 守护程序自身有单实例保护，重复拉起不会叠加进程。
func startGuard() {
	guard := filepath.Join(exeDir(), guardExeName())
	if _, err := os.Stat(guard); err != nil {
		slog.Info("未发现守护程序，以独立模式运行（崩溃后不会自动重启）", "expect", guard)
		return
	}
	cmd := exec.Command(guard, "-state", runStatePath())
	if err := cmd.Start(); err != nil {
		slog.Warn("守护程序启动失败，继续以独立模式运行", "path", guard, "err", err)
		return
	}
	// 不 Wait：守护程序需长期独立运行。
	// Release 让 os/exec 放弃对该进程的跟踪（避免 Unix 下残留僵尸进程）。
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	slog.Info("守护程序已启动", "path", guard)
}

func runStatePath() string {
	return filepath.Join(exeDir(), runStateName)
}

// reportAbnormalExit 检查上次运行是否异常终止。
//
// 背景（2026-09-01 通宵挂机事故）：进程在凌晨无声消失，所有账号任务停止，
// 但日志里既没有"退出"也没有任何 panic/错误，无法判断是程序崩溃还是外部原因
// （系统重启、Windows 更新、被任务管理器结束）。
//
// 判据：优雅退出会删除 run_state.json，因此**文件残留即代表上次是异常终止**。
// 有了"最后存活时刻"，就能与 Windows 事件日志（6006/6008/1074 等）对照：
// 若两者紧邻，说明是系统关机/重启（外部原因，应配置开机自启）；
// 若系统当时一直在运行，才需要按程序崩溃排查。
func reportAbnormalExit() {
	raw, err := os.ReadFile(runStatePath())
	if err != nil {
		return // 首次运行，或上次是优雅退出
	}
	var st runState
	if err := json.Unmarshal(raw, &st); err != nil || st.LastAlive == "" {
		return
	}
	last, err := time.ParseInLocation("2006-01-02 15:04:05", st.LastAlive, time.Local)
	if err != nil {
		return
	}
	gone := time.Since(last).Round(time.Second)
	slog.Error("检测到上次运行未正常退出（进程崩溃或被强杀）",
		"started", st.Started,
		"last_alive", st.LastAlive,
		"stopped_for", gone.String(),
		"hint", "将 last_alive 与系统事件日志(6006/6008/1074)对照：紧邻则为系统关机/重启（外部原因），否则按程序崩溃排查")
	writeStartupError(fmt.Sprintf(
		"上次运行异常终止：启动于 %s，最后存活 %s（已停止 %s）。\n"+
			"请将该时刻与 Windows 事件日志 6006/6008/1074 对照，判断是系统重启还是程序崩溃。",
		st.Started, st.LastAlive, gone))
}

// startLivenessBeat 写入并周期性刷新存活标记。
// 该文件有两大用途：
//  1. 下次启动时判定上次是否正常退出（崩溃取证）
//  2. 作为守护程序的监视依据（PID + 拉起所需的 exe/args + 心跳时间戳）
func startLivenessBeat(ctx context.Context, started time.Time) {
	exe, _ := os.Executable()
	st := runState{
		PID:     os.Getpid(),
		Exe:     exe,
		Args:    os.Args[1:],
		Started: started.Format("2006-01-02 15:04:05"),
	}
	write := func() {
		st.LastAlive = time.Now().Format("2006-01-02 15:04:05")
		raw, err := json.Marshal(st)
		if err != nil {
			return
		}
		_ = os.WriteFile(runStatePath(), raw, 0644)
	}
	write()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				buf := make([]byte, 8192)
				n := runtime.Stack(buf, false)
				slog.Error("存活心跳 goroutine panic", "panic", r, "stack", string(buf[:n]))
			}
		}()
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				write()
			}
		}
	}()
}

// defaultWebAddr Web 控制台默认监听地址（仅本机回环，外部不可访问）。
// 使用冷门端口，避免与常见端口冲突、减少被扫描探测。
const defaultWebAddr = "127.0.0.1:29090"

// resolveWebAddr 决定 Web 控制台监听地址，优先级：
// 命令行 -web 参数 > 配置文件 web_addr > 默认 127.0.0.1:29090。
func resolveWebAddr(flagAddr, cfgAddr string) string {
	if flagAddr != "" {
		return flagAddr
	}
	if cfgAddr != "" {
		return cfgAddr
	}
	return defaultWebAddr
}

// isLoopbackAddr 判断监听地址是否仅绑定本机回环（127.0.0.1 / localhost / ::1）。
// 回环地址外部无法访问，端口扫描也扫不到。
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "", "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// openBrowser 打开默认浏览器（兼容 Windows 7）
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		slog.Warn("自动打开浏览器失败", "err", err)
	}
}

// setupLogger 初始化日志输出：默认写入 logs/youjuhang.log（GUI 模式无控制台），
// -console 时输出到标准错误（调试用）。
func setupLogger(console bool) *os.File {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	useStderr := func() {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, opts)))
	}
	if console {
		useStderr()
		return nil
	}
	if err := os.MkdirAll(logDir(), 0755); err != nil {
		slog.Warn("创建日志目录失败，日志输出到控制台", "err", err)
		useStderr()
		return nil
	}
	f, err := os.OpenFile(logFilePath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		slog.Warn("打开日志文件失败，日志输出到控制台", "err", err)
		useStderr()
		return nil
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, opts)))
	return f
}

// resolveConfigPath 解析配置文件路径：
// 相对路径时优先取当前目录，若不存在则回退到 exe 所在目录（支持双击 exe 直接读取同目录配置）。
func resolveConfigPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if exe := filepath.Join(exeDir(), p); exe != p {
		if _, err := os.Stat(exe); err == nil {
			return exe
		}
	}
	return p
}

// exeDir 返回可执行文件所在目录；获取失败时回退到当前目录。
func exeDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}

// logDir 返回日志目录（exe 所在目录下的 logs）。
func logDir() string {
	return filepath.Join(exeDir(), "logs")
}

// logFilePath 返回日志文件路径。
func logFilePath() string {
	return filepath.Join(logDir(), "youjuhang.log")
}

// recoverPanic 捕获未处理异常并留痕：写错误文件 + 弹窗提示。
// windowsgui 模式下程序崩溃没有任何控制台输出，必须显式留痕，否则在旧系统上
// 会表现为"运行失败且无任何提示"。
func recoverPanic() {
	if r := recover(); r != nil {
		buf := make([]byte, 8192)
		n := runtime.Stack(buf, false)
		msg := fmt.Sprintf("程序发生未捕获异常：\n%v\n\n调用栈：\n%s", r, buf[:n])
		writeStartupError(msg)
		showStartupError(msg)
		os.Exit(2)
	}
}

// trace 追加一行启动跟踪到 exe 目录 startup_error.txt。
// 不依赖 slog（日志文件可能未初始化），用于定位 GUI 模式下程序执行到哪一步崩溃。
func trace(step string) {
	f, err := os.OpenFile(filepath.Join(exeDir(), "startup_error.txt"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(step + "\n")
	_ = f.Close()
}

// writeStartupError 把错误信息追加写入 exe 目录下的 startup_error.txt，便于排查。
func writeStartupError(msg string) {
	f, err := os.OpenFile(filepath.Join(exeDir(), "startup_error.txt"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	_, _ = f.WriteString("======== ERROR ========\n" + msg + "\n")
	_ = f.Close()
}

// 注意：showStartupError 按平台拆分实现，见：
//   startup_windows.go（//go:build windows）—— 弹 MessageBox 提示，GUI 模式无控制台时必需
//   startup_other.go  （//go:build !windows）—— 仅写 startup_error.txt，服务器环境以日志为准
// 拆分原因：NewLazyDLL/StringToUTF16Ptr 是 Windows 专有 API，写在共享文件里会导致
// GOOS=linux 交叉编译失败。此处不再定义同名函数，避免重复声明。
