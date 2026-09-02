package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRedirectStderrCreatesFile 重定向后目标文件必须存在（含自动建目录）。
//
// 这里只验证"文件被创建"，**不验证**内容落盘：
//   - os.Stderr 这个变量在包初始化时就绑定了**原始**句柄，SetStdHandle 之后
//     经 os.Stderr 的写入仍走原句柄，不会进我们的文件；
//   - 而 Go 运行时的 fatal error 是在崩溃**当场**调 GetStdHandle 取句柄，
//     因此会写进重定向后的文件——这正是本机制的目的，已用真实的
//     「并发 map 读写」崩溃实测确认（windowsgui 模式下完整捕获到堆栈）。
func TestRedirectStderrCreatesFile(t *testing.T) {
	// 不用 t.TempDir()：它会在测试结束时删除目录，而本函数必须**全程持有**
	// 文件句柄（否则被 GC 回收后崩溃时写不进去），持有期间文件被占用删不掉。
	// 这里改用手动临时目录，并在注册 RemoveAll 之后、断言之前注册"关闭句柄"，
	// 利用 defer 后进先出：先关句柄，再删目录。
	dir, err := os.MkdirTemp("", "stderr-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	p := filepath.Join(dir, "logs", "stderr.log") // 目录尚不存在，应自动创建
	redirectStderr(p)

	if keepStderrFile != nil {
		defer func() {
			_ = keepStderrFile.Close()
			keepStderrFile = nil
		}()
	}

	if _, err := os.Stat(p); err != nil {
		t.Fatalf("redirectStderr 未创建日志文件: %v", err)
	}
	// 句柄必须被持有，否则被 GC 回收后底层句柄关闭，崩溃时写不进去
	if keepStderrFile == nil {
		t.Fatal("redirectStderr 成功后必须把文件句柄保存到 keepStderrFile")
	}
}
