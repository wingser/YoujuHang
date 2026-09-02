//go:build !windows

package main

// showStartupError 非 Windows 平台（Linux/macOS）无 GUI 弹窗，仅写 startup_error.txt 留痕。
//
// 为什么不能用 syscall.NewLazyDLL("user32.dll")：
//   NewLazyDLL / StringToUTF16Ptr 是 Windows 专有 API，Linux 的 syscall 包里不存在，
//   直接写在 main.go 会让 GOOS=linux 交叉编译失败（undefined: syscall.NewLazyDLL）。
//   因此按平台拆分文件，Windows 走 startup_windows.go 的弹窗实现。
//
// 服务器环境下以日志为准：加 -console 参数可输出到标准错误，
// 否则写入 exe 同目录 logs/youjuhang.log。
func showStartupError(msg string) {
	writeStartupError(msg)
}
