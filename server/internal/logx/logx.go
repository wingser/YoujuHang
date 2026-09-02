// Package logx 提供按账号独立的 slog 日志（文件 + 控制台）
package logx

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Options 是日志配置
type Options struct {
	Dir      string // 日志目录
	Level    slog.Level
	Console  bool // 同时输出到控制台
	FileName string
}

// New 创建按账号独立的日志。account 为空则用默认名
func New(account string, opts Options) (*slog.Logger, error) {
	if opts.Dir == "" {
		opts.Dir = "logs"
	}
	if opts.FileName == "" {
		if account == "" {
			opts.FileName = "youjuhang.log"
		} else {
			opts.FileName = account + ".log"
		}
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("log dir: %w", err)
	}
	path := filepath.Join(opts.Dir, opts.FileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("log file %s: %w", path, err)
	}

	var writers []io.Writer
	writers = append(writers, f)
	if opts.Console {
		writers = append(writers, os.Stdout)
	}
	multi := io.MultiWriter(writers...)
	h := slog.NewTextHandler(multi, &slog.HandlerOptions{
		Level: opts.Level,
	})
	return slog.New(h), nil
}

// TimeText 返回本地时间文本，用于日志前缀
func TimeText() string {
	return time.Now().Format("2006-01-02 15:04:05")
}
