//go:build windows

package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
)

// STD_ERROR_HANDLE = (DWORD)-12
const stdErrorHandle = uint32(0xFFFFFFF4)

// keepStderrFile 持有重定向后的日志文件句柄。
// 必须全程持有：句柄关闭后标准错误写入会失败；
// 而且 *os.File 被 GC 回收时会 close 底层句柄，仅存局部变量会失效。
var keepStderrFile *os.File

// redirectStderr 把**进程级**标准错误句柄重定向到指定文件。
//
// 为什么必须做（2026-09-01 连续两晚「进程无声消失」事故）：
//  1. 程序用 -H windowsgui 构建，没有控制台，标准错误的输出被**直接丢弃**；
//  2. Go 的 **fatal error**（并发 map 读写、OOM、栈溢出等）无法被 recover()
//     捕获——运行时把错误信息与完整 goroutine 调用栈写到**文件描述符 2**
//     后直接终止进程，recover 完全插不上手；
//  3. 于是外部表现就是「进程无声消失、日志里什么都没有」，连续两晚无法定位。
//
// 实测验证（2026-09-01）：windowsgui 模式下先 SetStdHandle(STD_ERROR_HANDLE, 文件句柄)，
// 再触发并发 map 读写，"fatal error: concurrent map read and map write"
// 连同全部 goroutine 堆栈均完整落入目标文件。
//
// 必须在 main 最开头调用；成功后**不要关闭**文件句柄（由 keepStderrFile 持有）。
func redirectStderr(path string) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("SetStdHandle")
	if r, _, _ := proc.Call(uintptr(stdErrorHandle), f.Fd()); r == 0 {
		_ = f.Close()
		return
	}
	keepStderrFile = f
}

// checkWindowsRNG 启动早期探测 Go 的随机源兼容性，并确认兜底是否可用。
//
// 背景（2026-09-01 / 09-02 连续崩溃事故）：
// Go（含 1.21）的 crypto/rand 在 Windows 上直接调用 bcryptprimitives!ProcessPrng。
// **Win7、部分 Windows Server** 的 bcryptprimitives.dll 不导出 ProcessPrng ——
// 任何 crypto/rand.Read 调用（golang.org/x/net/http2 流重置时必调用）都会触发
// panic 终结进程，且 recover() 抓不到。
//
// **重要：把 bcryptprimitives.dll 拷到程序目录是无效的（2026-09-02 实测推翻旧结论）。**
// Go 通过 sysdll 机制登记该 DLL：`syscall.NewLazyDLL(sysdll.Add("bcryptprimitives.dll"))`
// 使其进入 `sysdll.IsSystemDLL`，dll_windows.go 随即改走 `loadsystemlibrary`，
// 即 `LoadLibraryEx(name, 0, LOAD_LIBRARY_SEARCH_SYSTEM32)`——**只搜 System32**，
// exe 目录与 PATH 一概被忽略（设计目的正是防 DLL 劫持，见 Go issue 14959）。
// 所以无论往程序目录放什么版本，加载到的永远是 System32 里那份缺导出的 DLL。
//
// 修复（见 rng_windows.go）：已在 main.init 阶段把 crypto/rand.Reader 换成
// advapi32!RtlGenRandom（SystemFunction036，XP 起存在）兜底——注意我们自己的
// `NewLazyDLL("advapi32.dll")` **没有**经 sysdll.Add 登记，走常规搜索路径，
// 且 advapi32 本身就在 System32 且必然含 SystemFunction036，因此兜底始终可用。
//
// 本函数只做探测 + 分级提示，返回 (随机源是否可用, 是否走的兜底)：
//   - ProcessPrng 可用 → INFO 通过，返回 (true, false)；
//   - ProcessPrng 缺失但 RtlGenRandom 可用 → WARN（已自动回退，不影响运行），返回 (true, true)；
//   - 两者都不可用 → ERROR（极罕见，需排查系统），返回 (false, true)。
func checkWindowsRNG() (bool, bool) {
	dll := syscall.NewLazyDLL("bcryptprimitives.dll")
	proc := dll.NewProc("ProcessPrng")
	// 用 Find() 而非 Addr()：缺失时返回错误而非 mustFind panic，以便优雅分支。
	if err := proc.Find(); err != nil {
		// ProcessPrng 缺失：确认兜底 RtlGenRandom 是否可用。
		if terr := testRtlGenRandom(); terr != nil {
			slog.Error("致命兼容性风险：bcryptprimitives.dll 缺失 ProcessPrng 导出，且 RtlGenRandom 兜底也不可用",
				"processprng_err", err, "fallback_err", terr)
			return false, true
		}
		slog.Warn("bcryptprimitives.dll 缺失 ProcessPrng 导出（Win7/旧 Server 常见）；已自动回退到 RtlGenRandom 兜底，crypto/rand 可正常工作，无需拷贝 DLL",
			"note", "拷贝 bcryptprimitives.dll 到程序目录无效：Go 经 sysdll 强制只从 System32 加载")
		return true, true
	}
	slog.Info("crypto/rand 依赖检查通过：bcryptprimitives!ProcessPrng 可用")
	return true, false
}
