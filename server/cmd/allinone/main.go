//go:build windows

// allinone 是把 diagnose.bat 的逻辑搬进 Go 程序内的一次性诊断工具。
//
// 为什么不用 bat：部分安全软件会拦截 .bat 脚本导致"闪退"，
// 而 exe 双击运行更可靠。所有阶段结果写入 C:\youjuhang\allinone.log，
// 并且每一步都用 MessageBox 弹窗确认，避免"窗口一闪看不到输出"。
//
// 判定方法（拿到 allinone.log 后看它写到第几行）：
//   - 一行都没有 → 程序在 main() 之前（runtime 初始化）崩溃 → 系统环境问题
//   - 有 [1/8] → runtime 正常
//   - 有 [5/8] 且显示 OK → 网络正常
//   - [6/8] 显示子进程输出 → 主程序崩溃点直接可见
package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

const logPath = "C:\\youjuhang\\allinone.log"

func logf(format string, args ...any) {
	line := fmt.Sprintf("%s  "+format, append([]any{time.Now().Format("15:04:05.000")}, args...)...)
	fmt.Println(line)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		_, _ = fmt.Fprintln(f, line)
		_ = f.Close()
	}
}

// msgbox 弹一个带确定按钮的消息框，点击后继续。
func msgbox(text, title string) {
	_, _, _ = syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(
		0,
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(text))),
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(title))),
		0x00000040, // MB_ICONINFORMATION | MB_OK
	)
}

func main() {
	_ = os.MkdirAll("C:\\youjuhang", 0755)
	_ = os.Remove(logPath)

	logf("[0/8] arch=%s 启动，main() 已执行 → runtime 初始化正常", runtime.GOARCH)

	logf("[2/8] MessageBox 测试 (user32.dll syscall)...")
	msgbox("[1/8] 通过：runtime 启动正常。\n[2/8] 这个框能弹出来 = user32 系统调用正常。\n\n点【确定】继续，后续每一步都会写日志。", "YoujuHang 诊断 2/8")
	logf("[2/8] MessageBox 已点击 → user32 OK")

	logf("[3/8] os.Executable...")
	exe, err := os.Executable()
	logf("[3/8] exe=%v err=%v", exe, err)

	logf("[4/8] 文件写入测试...")
	f, err := os.Create("C:\\youjuhang\\test-write.txt")
	if err != nil {
		logf("[4/8] 写文件失败: %v", err)
		msgbox("[4/8] 写文件失败！\n"+err.Error(), "失败")
	} else {
		_, _ = fmt.Fprintln(f, "write ok")
		_ = f.Close()
		logf("[4/8] 文件写入 OK")
	}

	logf("[5/8] net.Listen 测试 (Winsock)...")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		logf("[5/8] 网络监听失败: %v", err)
		msgbox("[5/8] 网络监听失败！\n"+err.Error(), "失败")
	} else {
		logf("[5/8] net.Listen OK: %v", ln.Addr())
		_ = ln.Close()
	}

	logf("[6/8] 启动 youjuhang-win64.exe 并捕获输出 (最多10s)...")
	selfDir := filepath.Dir(exe)
	child := filepath.Join(selfDir, "youjuhang-win64.exe")
	cmd := exec.Command(child, "-console", "-no-browser")
	var out []byte
	done := make(chan struct{})
	go func() {
		out, _ = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
		logf("[6/8] 子进程已退出")
	case <-time.After(10 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		logf("[6/8] 10 秒未退出，已终止（程序在正常运行而非崩溃）")
	}
	logf("[6/8] 子进程输出:\n%s", string(out))

	logf("[7/8] 检查 startup_error.txt...")
	se, err := os.ReadFile(filepath.Join(selfDir, "startup_error.txt"))
	if err != nil {
		logf("[7/8] startup_error.txt 不存在 (%v)", err)
	} else {
		logf("[7/8] startup_error.txt 内容:\n%s", string(se))
	}

	logf("[8/8] 全部完成！请把 C:\\youjuhang\\allinone.log 发给开发者")
	msgbox("[8/8] 全部步骤完成。\n\n请把 C:\\youjuhang\\allinone.log 的内容发给开发者。\n（文件位置：C:\\youjuhang\\allinone.log）", "YoujuHang 诊断完成")
}
