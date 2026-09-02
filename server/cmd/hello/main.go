//go:build windows

// hello 是极致最小兼容性测试：不 import net/http 等任何网络库，
// 只做一件事——在 main() 里写一行文件，然后 sleep 3 秒。
//
// 判定方法：
//   - 若 C:\youjuhang\hello.log 存在且内容为 "hello start" → main() 已正常执行，
//     runtime 完全正常，问题在后续的 net/http 或 walk 链路。
//   - 若 hello.log 不存在 → 程序在 main() 之前（runtime 初始化 / 包 init）就崩溃了，
//     说明是 Go runtime 与这台机器的系统环境不兼容。
package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	path := "C:\\youjuhang\\hello.log"
	f, err := os.Create(path)
	if err == nil {
		_, _ = fmt.Fprintln(f, "hello start")
		_ = f.Sync()
		_ = f.Close()
	}
	time.Sleep(3 * time.Second)
}
