//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// showStartupError 在 Windows GUI 模式下用消息框提示启动错误。
// -H windowsgui 模式下程序崩溃没有任何控制台输出，用户看不到任何信息，
// 必须弹窗提示；同时同步写一份 startup_error.txt 留痕。
func showStartupError(msg string) {
	writeStartupError(msg)
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("MessageBoxW")
	proc.Call(0,
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(msg))),
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("游聚挂机 - 启动失败"))),
		0x10) // MB_ICONERROR
}
