//go:build windows

// diagwin7 是 Windows 7 兼容性二分诊断程序（不依赖任何 GUI 库）。
//
// 用途：定位 youjuhang 在 Win7 上"运行即崩溃、无任何输出"的问题层次。
//   - 如果本程序在 Win7 上正常运行（diagwin7.log 有 heartbeat）→ 崩溃与
//     walk/tray 图形库相关，问题在 GUI 初始化链。
//   - 如果本程序同样崩溃（diagwin7.log 只到某一行）→ 崩溃在 Go runtime /
//     标准库层面（更底层的兼容性问题）。
//
// 运行方式：双击或命令行执行，日志写入同目录 diagwin7.log，同时输出到 stderr。
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	dir, err := os.Executable()
	if err != nil {
		dir = "."
	} else {
		dir = filepath.Dir(dir)
	}
	logf := filepath.Join(dir, "diagwin7.log")
	f, err := os.OpenFile(logf, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		os.Stderr.WriteString("open diagwin7.log failed: " + err.Error() + "\n")
		os.Exit(1)
	}
	defer f.Close()

	log := func(s string) {
		line := time.Now().Format("2006-01-02 15:04:05.000 ") + s + "\r\n"
		_, _ = f.WriteString(line)
		os.Stderr.WriteString(line)
	}

	log("=== diagwin7 start (PID " + fmt.Sprint(os.Getpid()) + ") ===")

	// 测试 slog 文件日志（与主程序 setupLogger 相同路径）
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, opts)))
	slog.Info("slog ready")

	// 测试 net/http（与主程序 web 包相同的用法）
	srv := &http.Server{Addr: "127.0.0.1:29091"}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	go func() {
		log("http ListenAndServe: " + srv.Addr)
		if err := srv.ListenAndServe(); err != nil {
			log("http err: " + err.Error())
		}
	}()

	// 心跳 30 秒，验证 runtime 稳定运行
	for i := 1; i <= 15; i++ {
		time.Sleep(2 * time.Second)
		log(fmt.Sprintf("heartbeat %d/15", i))
	}
	log("=== diagwin7 done (no crash) ===")
}
