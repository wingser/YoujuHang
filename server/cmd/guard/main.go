// youjuhang-guard 是 youjuhang 主程序的守护进程（独立 exe，与主程序同目录发布）。
//
// 为什么需要独立进程（2026-09-01 连续两晚崩溃事故）：
// 主程序崩溃后进程消失，所有账号挂机任务全部停止，且因 -H windowsgui 无控制台、
// stderr 被丢弃，日志里不留任何痕迹，第二天才发现全晚没挂上。
// 守护进程自身极简（只做"看心跳 + 拉起"），崩溃概率远低于主程序。
//
// 职责：
//   - 每分钟读取主程序写入的 run_state.json（存活心跳）
//   - 心跳超时且主进程已消失 → 判定崩溃，立即重新拉起
//   - 心跳超时但主进程仍在 → 判定假死，二次确认后杀掉再拉起
//   - 主程序优雅退出（会删除 run_state.json）→ 守护进程随之退出，不打扰用户
//
// 设计约束：
//   - 主程序必须能在没有本程序时独立运行（本程序是可选增强，非依赖）
//   - 单实例：主程序每次启动都会尝试拉起本程序，重复拉起不能叠加进程
//   - 拉起时强制 -no-browser：服务端/后台场景不应反复弹浏览器
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 默认节奏常量，可用命令行参数覆盖（便于测试与按环境调优）
const (
	// defaultCheckInterval 检查间隔。
	// 设为 15s：主程序心跳 1 分钟一次，但守护仅靠此间隔感知"主程序已退出"或
	// "PID 已消失"等事件——15s 是兼顾开销与感知速度的折中（每小时约 240 次读）。
	defaultCheckInterval = 15 * time.Second
	// defaultStaleAfter 主程序心跳超过该时长判定为失活。
	//
	// 2026-10-08 由 5 分钟放宽到 10 分钟：实测机器睡眠 8.5 分钟就会超过 5 分钟阈值，
	// 使守护在醒来后把健康的进程当假死强杀（见 run 中的"自身被挂起"识别）。
	// 现在已有更可靠的判据兜底，这里再放宽阈值做双保险——真正的假死（进程活着但
	// 业务停摆）多等 5 分钟代价很小，而误杀健康进程会中断全部账号的挂机。
	defaultStaleAfter = 10 * time.Minute
	// relaunchGrace 拉起后等待主程序就绪（写入新心跳）的宽限期
	relaunchGrace = 90 * time.Second
	// guardLogName 守护进程日志文件名（落在主程序 logs 目录下）
	guardLogName = "guard.log"
	// lockName 单实例锁文件名
	lockName = "guard.lock"
)

// runState 与主程序 cmd/youjuhang 中的 runState 结构保持一致（字段是其子集）
type runState struct {
	PID       int      `json:"pid"`
	Exe       string   `json:"exe"`
	Args      []string `json:"args"`
	Started   string   `json:"started"`
	LastAlive string   `json:"last_alive"`
}

var (
	log           *slog.Logger
	checkInterval time.Duration
	staleAfter    time.Duration
)

func main() {
	statePath := flag.String("state", "", "主程序存活标记文件路径（run_state.json）")
	flag.DurationVar(&checkInterval, "check-interval", defaultCheckInterval, "检查主程序心跳的间隔")
	flag.DurationVar(&staleAfter, "stale-after", defaultStaleAfter,
		"心跳超过该时长判定主程序失活（建议不小于心跳周期的 3 倍，避免误判）")
	flag.Parse()
	if *statePath == "" {
		fmt.Fprintln(os.Stderr, "youjuhang-guard: 缺少 -state 参数")
		os.Exit(2)
	}

	log = setupLogger()
	defer recoverPanic()

	if !acquireLock() {
		log.Info("已有守护进程在运行，本实例退出（单实例保护）")
		return
	}
	defer releaseLock()

	// 写下自己的 PID，供主程序在**主动退出**时直接结束本进程。
	// 否则主程序退出后只能等本进程下一次轮询发现 run_state.json 消失才退出，
	// 期间守护进程仍然存活（2026-09-01 用户反馈）。
	writePIDFile()
	defer removePIDFile()

	log.Info("守护进程启动",
		"state", *statePath,
		"check_interval", checkInterval.String(),
		"stale_after", staleAfter.String())

	run(context.Background(), *statePath)
	log.Info("守护进程退出")
}

// decide 根据状态、PID、上次心跳返回下一步动作。
// PID 优先：死进程不会自愈，等 staleAfter=5min 没意义；
// 崩溃后 1 个 check-interval（默认 15s）就拉起，修复 Bug3。
func decide(st *runState, lastAlive time.Time, now time.Time, suspiciousIn guardAction) guardAction {
	if !processAlive(st.PID) {
		return actionCrash
	}
	stale := now.Sub(lastAlive)
	if stale < staleAfter {
		return actionWait
	}
	if suspiciousIn == actionWait {
		return actionSuspectHang
	}
	return actionConfirmHang
}

// guardAction 守护进程单次检查的决策结果
type guardAction int

const (
	actionWait guardAction = iota
	actionSuspectHang
	actionConfirmHang
	actionCrash
)

// run 监视主循环
func run(ctx context.Context, statePath string) {
	// 当前轮决策结果（同时充当下一轮的 suspicious 输入；actionWait 表示无需确认）
	suspicious := actionWait
	// lastCheck 本轮检查的开始时刻，用于识别"守护进程自身也被挂起过"。
	lastCheck := time.Now()

	for {
		// 系统休眠/睡眠识别（2026-10-08 修复）。
		//
		// 实测事故（2026-10-03）：机器睡眠约 8.5 分钟（心跳最后 15:10:40，
		// 守护 15:19:17 才恢复运行）。醒来后守护看到心跳已过期 8m37s，
		// 超过 staleAfter(5m) 阈值 → 二次确认 → **强杀并重启了本来健康的进程**，
		// 三个账号的挂机被迫中断。
		//
		// 判据：守护自身每 checkInterval（默认 15s）跑一轮，若本轮距上轮远超该间隔，
		// 说明**连守护自己都被挂起**了——此时"心跳过期"是共同现象（进程被一起冻结），
		// 而非主程序假死。这是比 staleAfter 阈值更可靠的信号：无论睡眠多久都能识别。
		now := time.Now()
		if gap := now.Sub(lastCheck); gap > checkInterval*3 {
			log.Info("检测到守护进程自身被挂起（系统休眠/睡眠？），跳过本轮假死判定",
				"gap", gap.Round(time.Second).String(),
				"check_interval", checkInterval.String())
			suspicious = actionWait
			lastCheck = now
			waitNext(ctx)
			continue
		}
		lastCheck = now

		st, err := loadState(statePath)
		if err != nil {
			// 文件不存在 = 主程序优雅退出（退出前会删除该文件）
			if os.IsNotExist(err) {
				log.Info("存活标记已消失，判定主程序正常退出，守护进程结束")
				return
			}
			log.Warn("读取存活标记失败，下一轮重试", "err", err)
			waitNext(ctx)
			continue
		}

		last, err := time.ParseInLocation("2006-01-02 15:04:05", st.LastAlive, time.Local)
		if err != nil {
			log.Warn("存活标记时间格式非法，下一轮重试", "value", st.LastAlive, "err", err)
			waitNext(ctx)
			continue
		}

		// 决策以 **PID 优先**：
		//   进程没了 = 明确崩溃 → 立即拉起（不必等 5 分钟 stale 阈值）。
		//   进程还在 + 心跳新鲜 → 正常，等。
		//   进程还在 + 心跳过期 → 疑似假死 → 二次确认后再杀+重启。
		// 这样 Bug3「崩溃后守护不拉起」从最多 5 分钟压到 1 个检查周期（默认 15s）。
		suspicious = decide(st, last, time.Now(), suspicious)
		switch suspicious {
		case actionWait:
			waitNext(ctx)
		case actionSuspectHang:
			log.Warn("心跳过期但主进程仍在，疑似假死或系统刚休眠唤醒，下一轮确认",
				"pid", st.PID, "last_alive", st.LastAlive)
			waitNext(ctx)
		case actionConfirmHang:
			log.Error("连续两次检测到心跳过期且主进程仍在，判定主程序假死，强制重启",
				"pid", st.PID, "last_alive", st.LastAlive)
			killProcess(st.PID)
			relaunch(st, statePath)
			waitNext(ctx)
		case actionCrash:
			log.Error("检测到主程序已崩溃（进程消失）",
				"pid", st.PID, "started", st.Started,
				"last_alive", st.LastAlive)
			relaunch(st, statePath)
			waitNext(ctx)
		}
	}
}

// relaunch 重新拉起主程序，并等待其写入新的心跳。
// 拉起时强制追加 -no-browser：后台/服务端场景不应反复弹出浏览器窗口。
func relaunch(st *runState, statePath string) {
	if st.Exe == "" {
		log.Error("存活标记缺少 exe 路径，无法拉起主程序")
		return
	}
	if _, err := os.Stat(st.Exe); err != nil {
		log.Error("主程序可执行文件不存在，放弃拉起", "exe", st.Exe, "err", err)
		return
	}

	args := append([]string{}, st.Args...)
	if !containsFlag(args, "no-browser") {
		args = append(args, "-no-browser")
	}

	cmd := exec.Command(st.Exe, args...)
	cmd.Dir = filepath.Dir(st.Exe)
	// 重定向子进程的 stdout/stderr 到日志：主程序是 windowsgui 模式（无控制台），
	// Go 的 fatal error（并发 map/OOM，recover 抓不到）输出走 fd 2，
	// 不接管就会丢失，导致崩溃原因无从查起。
	// 主程序自身也会重定向一次，这里是双保险（覆盖守护进程拉起的实例）。
	if f, err := os.OpenFile(stderrLogPath(cmd.Dir),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		cmd.Stdout = f
		cmd.Stderr = f
		defer func() { _ = f.Close() }()
	} else {
		log.Warn("无法打开 stderr 日志，子进程崩溃原因将丢失", "err", err)
	}
	if err := cmd.Start(); err != nil {
		log.Error("重新拉起主程序失败", "exe", st.Exe, "args", args, "err", err)
		return
	}
	if cmd.Process != nil {
		_ = cmd.Process.Release() // 不跟踪子进程，避免 Unix 下残留僵尸
	}
	log.Info("已重新拉起主程序", "exe", st.Exe, "args", args, "new_pid", cmd.Process.Pid)

	// 等待主程序就绪：以"写入了更新的心跳"为准
	deadline := time.Now().Add(relaunchGrace)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		if cur, err := loadState(statePath); err == nil && cur.LastAlive != st.LastAlive {
			log.Info("主程序已恢复运行", "new_pid", cur.PID, "last_alive", cur.LastAlive)
			return
		}
	}
	log.Warn("拉起后等待就绪超时，继续常规监视", "grace", relaunchGrace.String())
}

// processAlive / killProcess 按平台实现，见 process_windows.go / process_other.go

// killProcess 强制结束指定 PID
func killProcess(pid int) {
	p, err := os.FindProcess(pid)
	if err != nil {
		log.Warn("查找待结束进程失败", "pid", pid, "err", err)
		return
	}
	if err := p.Kill(); err != nil {
		log.Warn("结束进程失败", "pid", pid, "err", err)
		return
	}
	log.Info("已结束假死进程", "pid", pid)
}

func loadState(path string) (*runState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st runState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func waitNext(ctx context.Context) {
	t := time.NewTimer(checkInterval)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func containsFlag(args []string, name string) bool {
	for _, a := range args {
		a = strings.TrimLeft(a, "-")
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

// ---------- 单实例锁 ----------

var (
	lockPath string
	lockOnce sync.Mutex
)

// acquireLock 获取单实例锁。
// 主程序每次启动都会尝试拉起守护进程，若无保护会不断叠加进程。
// 锁文件残留（上次守护进程异常退出）时，检查其中 PID 是否存活：
// 不存活则接管，存活则本实例退出。
func acquireLock() bool {
	lockOnce.Lock()
	defer lockOnce.Unlock()

	lockPath = filepath.Join(exeDir(), lockName)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return true
		}
		if !os.IsExist(err) {
			log.Warn("无法创建守护锁文件，以无锁方式继续", "path", lockPath, "err", err)
			lockPath = ""
			return true
		}
		// 锁已存在：判断持有者是否还活着
		raw, rerr := os.ReadFile(lockPath)
		if rerr != nil {
			return false
		}
		var pid int
		if _, serr := fmt.Sscan(strings.TrimSpace(string(raw)), &pid); serr != nil || pid <= 0 {
			// 内容损坏：删除后重试一次
			_ = os.Remove(lockPath)
			continue
		}
		if processAlive(pid) {
			return false // 已有守护进程在运行
		}
		// 持有者已死：接管锁
		log.Info("清理残留的守护锁", "dead_pid", pid)
		_ = os.Remove(lockPath)
	}
}

func releaseLock() {
	lockOnce.Lock()
	defer lockOnce.Unlock()
	if lockPath != "" {
		_ = os.Remove(lockPath)
	}
}

// ---------- 基础工具 ----------

func exeDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}

// stderrLogPath 主程序崩溃输出（fatal error 调用栈）的落盘路径。
// 与主程序 cmd/youjuhang 的 logDir()/stderr.log 保持一致。
func stderrLogPath(dir string) string {
	return filepath.Join(dir, "logs", "stderr.log")
}

// guardPIDFileName 守护进程 PID 文件名。
// 主程序退出时读取它，直接结束守护进程，避免"主程序已退出、守护进程还活着"。
const guardPIDFileName = "guard.pid"

// guardPIDPath 返回 PID 文件路径（与主程序同目录）。
// 用 state 文件所在目录更可靠：主程序与守护进程一定在同一目录。
func guardPIDPath() string {
	dir := exeDir()
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, guardPIDFileName)
}

// writePIDFile 写下当前进程 PID
func writePIDFile() {
	_ = os.WriteFile(guardPIDPath(), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// removePIDFile 删除 PID 文件（进程正常退出时）
func removePIDFile() {
	_ = os.Remove(guardPIDPath())
}

// setupLogger 日志写入 <exeDir>/logs/guard.log。
// 守护进程以 -H windowsgui 构建（无控制台），不落盘则任何异常都无法追溯。
func setupLogger() *slog.Logger {
	dir := filepath.Join(exeDir(), "logs")
	_ = os.MkdirAll(dir, 0755)
	f, err := os.OpenFile(filepath.Join(dir, guardLogName),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	return slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func recoverPanic() {
	if r := recover(); r != nil {
		buf := make([]byte, 16384)
		n := runtime.Stack(buf, false)
		log.Error("守护进程发生未捕获异常，即将退出",
			"panic", r, "stack", string(buf[:n]))
	}
}
