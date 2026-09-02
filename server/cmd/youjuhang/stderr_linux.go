//go:build linux

// Linux 专用：用 Dup3 复制文件描述符 2。
//
// 为什么不能与 darwin/BSD 共用 Dup2：
// **Linux arm64 内核没有 Dup2 系统调用**（arm64 只提供 Dup3），
// Go 的 syscall 包因此在 linux/arm64 上不导出 syscall.Dup2。
// 若统一用 Dup2，交叉编译 linux/arm64 会直接失败（undefined: syscall.Dup2）。
//
// Dup3(oldfd, newfd, flags) 与 Dup2(oldfd, newfd) 等价（flags=0 时），
// 且 linux/amd64 与 linux/arm64 均提供，故 Linux 统一用 Dup3。

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// keepStderrFile 持有重定向后的日志文件句柄（防止被 GC 回收关闭）
var keepStderrFile *os.File

// checkWindowsRNG 非 Windows 平台无需检测：
// `bcryptprimitives!ProcessPrng` 是 Windows 专属问题（Linux 走 getrandom，
// 不存在"过程找不到"的情况）。必须与 stderr_windows.go 的同名函数成对存在，
// 否则 main() 里的无条件调用会在交叉编译时链接失败。
func checkWindowsRNG() (bool, bool) {
	// Linux 随机源由内核保证，无需检测，也谈不上兜底
	return true, false
}

// redirectStderr 把文件描述符 2（标准错误）重定向到指定文件。
//
// 目的与 Windows 版一致：让 Go 运行时的 fatal error（并发 map 读写、OOM 等）
// 输出与调用栈落盘，而不是丢到没有控制台的地方。详见 stderr_windows.go 注释。
func redirectStderr(path string) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	if err := syscall.Dup3(int(f.Fd()), 2, 0); err != nil {
		_ = f.Close()
		return
	}
	keepStderrFile = f
}
