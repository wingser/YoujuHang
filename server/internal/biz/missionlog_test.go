package biz

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestLogMissionListSuppressesUnchanged 验证任务明细日志的降噪行为。
//
// 背景（2026-10-08）：任务明细每分钟 52 行、占账号日志 93%~95%，
// 每天每账号约 6MB，长期挂机会吃光磁盘并导致日志静默失败。
// 降噪规则：只在「任务 ID + 完成状态(f3)」变化时打印逐条明细。
func TestLogMissionListSuppressesUnchanged(t *testing.T) {
	var buf bytes.Buffer
	w := &Worker{log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}

	missions := []Mission{
		{ID: 2002, Field2: 100, Field3: 0},
		{ID: 2000, Field2: 7200, Field3: 1},
	}

	// 1. 首次调用：必须打印明细（否则排查时看不到初始状态）
	w.logMissionList(missions)
	first := buf.String()
	if !strings.Contains(first, `msg="  任务"`) || !strings.Contains(first, "changed=true") {
		t.Fatalf("首次调用应打印明细且标记 changed=true，实际: %q", first)
	}
	firstLen := buf.Len()

	// 2. 仅进度 f2 变化（时长类任务每分钟都在涨）：不应重复打印明细
	buf.Reset()
	missions[0].Field2 = 160
	missions[1].Field2 = 7200
	w.logMissionList(missions)
	got := buf.String()
	if strings.Contains(got, `msg="  任务"`) {
		t.Errorf("仅进度变化时不应打印逐条明细，实际: %q", got)
	}
	if !strings.Contains(got, "changed=false") {
		t.Errorf("应输出一行摘要并标记 changed=false，实际: %q", got)
	}
	if buf.Len() >= firstLen {
		t.Errorf("降噪后输出应显著变小：首次 %d 字节，本次 %d 字节", firstLen, buf.Len())
	}

	// 3. 完成状态 f3 变化：必须打印明细（这是真正需要关注的事件）
	buf.Reset()
	missions[0].Field3 = 1
	w.logMissionList(missions)
	got = buf.String()
	if !strings.Contains(got, `msg="  任务"`) || !strings.Contains(got, "changed=true") {
		t.Errorf("完成状态变化时应打印明细并标记 changed=true，实际: %q", got)
	}

	// 4. 任务增删（ID 集合变化）也应触发明细
	buf.Reset()
	w.logMissionList(append(missions, Mission{ID: 2005, Field3: 0}))
	if !strings.Contains(buf.String(), "changed=true") {
		t.Errorf("任务数量变化应触发明细，实际: %q", buf.String())
	}
}
