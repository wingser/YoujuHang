//go:build windows

package main

import "syscall"

// stillActive Windows 上 GetExitCodeProcess 对仍在运行的进程返回的伪退出码
const stillActive = 259

// processQueryLimitedInformation 查询进程退出码所需的最小权限（Vista+ 支持，
// 且对本用户启动的进程不会被拒绝，优于较老的 PROCESS_QUERY_INFORMATION）
const processQueryLimitedInformation = 0x1000

// processAlive 判断指定 PID 的进程是否仍在运行。
//
// 为什么不能直接用 os.FindProcess：
// Windows 上进程退出后，其内核对象只要还有打开的句柄就不会销毁，
// 此时 OpenProcess 依然可能成功 → os.FindProcess 对**已退出的进程也返回成功**。
// 实测（2026-09-01 守护进程联调）：主程序 os.Exit(3) 崩溃后，FindProcess 仍报告存活，
// 导致守护进程把"崩溃"误判成"假死"，多等一轮确认还去 Kill 一个已死的进程
// （TerminateProcess: Access is denied）。
//
// 正确做法：拿到句柄后取退出码，仍在运行时返回伪退出码 STILL_ACTIVE(259)。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
