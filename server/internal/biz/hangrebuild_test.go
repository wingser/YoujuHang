package biz

import (
	"testing"
	"time"
)

// TestTryRebuildHangDailyLimit 验证挂机会话重建的当日次数上限与跨天重置。
//
// 背景（2026-10-07）：chouyoku 的战队游戏时长在服务端侧卡死不增长时，
// 挂机监督原先只会一直等（白挂 18 小时），现改为超时未达标时重建会话。
// 上限用于防止服务端持续限制时演变成无限重连循环。
func TestTryRebuildHangDailyLimit(t *testing.T) {
	w := &Worker{}

	for i := 1; i <= maxHangRebuildsPerDay; i++ {
		if !w.tryRebuildHang() {
			t.Fatalf("第 %d 次重建应被允许（上限 %d）", i, maxHangRebuildsPerDay)
		}
	}
	if w.tryRebuildHang() {
		t.Fatalf("超过当日上限 %d 后应拒绝重建，否则会变成无意义的重连循环", maxHangRebuildsPerDay)
	}

	// 模拟跨天：日期 key 变化后应重新获得额度
	w.hangRebuildDay = dayKey(time.Now().AddDate(0, 0, -1))
	if !w.tryRebuildHang() {
		t.Error("跨天后应重新允许重建")
	}
}
