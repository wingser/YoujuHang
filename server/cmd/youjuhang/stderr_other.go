//go:build !windows && !linux

// 本文件覆盖 darwin / BSD 等"有 Dup2、无 Dup3"的 Unix 平台。
// Linux 单独见 stderr_linux.go——因为 **Linux arm64 没有 Dup2 系统调用**
// （只有 Dup3），统一用 Dup2 会导致 linux/arm64 交叉编译失败。

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// keepStderrFile 持有重定向后的日志文件句柄（防止被 GC 回收关闭）
var keepStderrFile *os.File

// checkWindowsRNG 非 Windows 平台无需检测：
// `bcryptprimitives!ProcessPrng` 是 Windows 专属问题（Go 在 Unix 上走
// /dev/urandom 或 getrandom，不存在"过程找不到"的情况）。
//
// 必须与 stderr_windows.go 的同名函数**成对存在**：main() 无条件调用它，
// 缺一个实现会导致交叉编译到非 Windows 平台时链接失败（undefined）。
func checkWindowsRNG() (bool, bool) {
	// Unix 上随机源由内核保证，无需检测，也谈不上兜底
	return true, false
}

// redirectStderr 把文件描述符 2（标准错误）重定向到指定文件。
//
// 目的与 Windows 版一致：让 Go 运行时的 fatal error（并发 map 读写、OOM 等）
// 输出与调用栈落盘，而不是丢到没有控制台的地方。详见 stderr_windows.go 注释。
//
// Unix 上用 Dup2 直接替换 fd 2，语义确定、不依赖运行时内部实现。
func redirectStderr(path string) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	if err := syscall.Dup2(int(f.Fd()), 2); err != nil {
		_ = f.Close()
		return
	}
	keepStderrFile = f
}
