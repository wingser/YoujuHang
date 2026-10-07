package room

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestNonOKTrackerThrottle 验证「非 ok 返回码」的日志节流（2026-10-07 诊断增强）。
//
// 背景：1028 心跳 / 1042 在线上报的非 ok 返回码原先只记 Debug，默认不输出，
// 导致「服务端不再累计游戏时长」这类问题在日志里完全不可见——chouyoku 10/7
// 的战队游戏时长卡在 807 秒后再不增长、白挂 18 小时，日志却显示一切正常。
// 但也不能直接改 Warn：消息约每秒一条，持续异常会刷爆日志，故需节流。
func TestNonOKTrackerThrottle(t *testing.T) {
	var buf bytes.Buffer
	lg := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	var tr nonOKTracker
	const bad = uint32(4294967286) // -10，服务端常见的 token 失效码

	// 1. 正常返回码不产生日志
	tr.log(lg, "m", retOK)
	if buf.Len() != 0 {
		t.Fatalf("retOK(=1) 不应记录，实际: %q", buf.String())
	}

	// 2. 首次异常立即记录（含返回码与次数）
	tr.log(lg, "m", bad)
	if got := buf.String(); !strings.Contains(got, "ret=4294967286") || !strings.Contains(got, "times=1") {
		t.Fatalf("首次异常应记录 ret 与 times=1，实际: %q", got)
	}

	// 3. 持续相同返回码不应逐条刷屏（58 次仍无新日志）
	buf.Reset()
	for i := 0; i < 58; i++ {
		tr.log(lg, "m", bad)
	}
	if buf.Len() != 0 {
		t.Fatalf("持续相同的返回码不应刷屏，实际: %q", buf.String())
	}

	// 4. 累计到第 60 次时汇总记一条
	tr.log(lg, "m", bad)
	if got := buf.String(); !strings.Contains(got, "times=60") {
		t.Fatalf("第 60 次应汇总记录 times=60，实际: %q", got)
	}

	// 5. 返回码变化时立即记录
	buf.Reset()
	tr.log(lg, "m", uint32(4294967295)) // -1
	if got := buf.String(); !strings.Contains(got, "times=1") {
		t.Fatalf("返回码变化时应立即记录，实际: %q", got)
	}

	// 6. 恢复正常后计数复位：再次异常应从 times=1 重新开始
	buf.Reset()
	tr.log(lg, "m", retOK)
	tr.log(lg, "m", bad)
	if got := buf.String(); !strings.Contains(got, "times=1") {
		t.Fatalf("恢复后再次异常应从 times=1 重新计数，实际: %q", got)
	}
}
