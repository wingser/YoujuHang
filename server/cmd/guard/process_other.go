//go:build !windows

package main

import (
	"os"
	"syscall"
)

// processAlive 判断指定 PID 的进程是否仍在运行（Unix 版本）。
//
// Unix 上 os.FindProcess 几乎总是成功（只是构造一个 Process 对象），
// 必须发 0 信号探测真实存活：进程不存在时返回 ESRCH。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
