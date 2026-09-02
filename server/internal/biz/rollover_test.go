package biz

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestStateStoreCrossDay 验证 StateStore 当日捐献跨天清空：
// 进程长跑不重启时，新一天必须重新从 0 开始捐献。
//
// 这就是 2026-08-31 当时的 ContributeStore 跨天 bug 留下的回归测试（详见 contrib.go 注释）。
func TestStateStoreCrossDay(t *testing.T) {
	dir := t.TempDir()
	s := NewStateStore(filepath.Join(dir, "task_state.json"))

	// 模拟"昨天捐了 3000"
	s.mu.Lock()
	s.data.Contribute.Date = "2020-01-01"
	s.data.Contribute.ByUID = map[uint32]int{123: 3000}
	s.mu.Unlock()

	if got := s.ContributeToday(123); got != 0 {
		t.Fatalf("跨天后 ContributeToday() 应为 0，实际 %d —— 新一天将不再捐献", got)
	}

	// 同一天内累加应保留
	if err := s.ContributeAdd(123, 500); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := s.ContributeToday(123); got != 500 {
		t.Fatalf("同日累加后应为 500，实际 %d", got)
	}
	if err := s.ContributeAdd(123, 300); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := s.ContributeToday(123); got != 800 {
		t.Fatalf("同日再累加后应为 800，实际 %d", got)
	}

	// 再次模拟跨天
	s.mu.Lock()
	s.data.Contribute.Date = "2020-01-02"
	s.mu.Unlock()
	if got := s.ContributeToday(123); got != 0 {
		t.Fatalf("再次跨天后应为 0，实际 %d", got)
	}
}

// TestStateStoreBakBackup 验证每次保存前生成 .bak 备份
func TestStateStoreBakBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task_state.json")
	s := NewStateStore(path)

	if err := s.ContributeAdd(6017780, 1000); err != nil {
		t.Fatal(err)
	}
	// 第一次写入后：主文件应有内容，.bak 不应存在（首次保存时源文件为空）
	firstMain, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstMain) == 0 {
		t.Fatal("主文件未写入")
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("首次保存不应有 .bak（源文件为空，无可备份内容）")
	}

	// 第二次写入应备份第一次的内容到 .bak
	if err := s.ContributeAdd(5571561, 2000); err != nil {
		t.Fatal(err)
	}
	secondMain, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("第二次写入应生成 .bak: %v", err)
	}
	if string(bak) != string(firstMain) {
		t.Error(".bak 内容应等于第一次的主文件内容")
	}
	if string(secondMain) == string(firstMain) {
		t.Error("第二次写入后主文件应变化")
	}
	if string(bak) == string(secondMain) {
		t.Error(".bak 不应等于第二次的主文件（应是第一份的）")
	}
}

// TestStateStoreLegacyMigration 验证启动时从旧文件迁移
func TestStateStoreLegacyMigration(t *testing.T) {
	dir := t.TempDir()

	// 写一个旧格式的 contribute_state.json
	oldContrib := `{"date":"2026-09-01","by_uid":{"5571561":1300,"6017780":3000}}`
	if err := os.WriteFile(filepath.Join(dir, "contribute_state.json"),
		[]byte(oldContrib), 0644); err != nil {
		t.Fatal(err)
	}
	// 写一个旧格式的 mall_state.json
	oldMall := `{"by_uid":{"6017780":{"claimed_month":202609,"last_info":"已领取"}}}`
	if err := os.WriteFile(filepath.Join(dir, "mall_state.json"),
		[]byte(oldMall), 0644); err != nil {
		t.Fatal(err)
	}

	// 创建 StateStore：应自动迁移并删除旧文件
	pinNow(t, "2026-09-01")
	newPath := filepath.Join(dir, "task_state.json")
	s := NewStateStore(newPath)

	// 验证迁移成功
	if got := s.ContributeToday(5571561); got != 1300 {
		t.Errorf("ContributeToday(5571561) = %d, want 1300", got)
	}
	if got := s.ContributeToday(6017780); got != 3000 {
		t.Errorf("ContributeToday(6017780) = %d, want 3000", got)
	}
	if !s.MallIsClaimed(6017780, 202609) {
		t.Error("商城已领取状态未迁移")
	}
	if got := s.MallLastInfo(6017780); got != "已领取" {
		t.Errorf("MallLastInfo = %q, want %q", got, "已领取")
	}

	// 验证新文件已创建
	if _, err := os.Stat(newPath); err != nil {
		t.Error("新 task_state.json 应已创建")
	}

	// 验证旧文件已被删除（迁移完成后）
	if _, err := os.Stat(filepath.Join(dir, "contribute_state.json")); err == nil {
		t.Error("旧 contribute_state.json 应被删除")
	}
	if _, err := os.Stat(filepath.Join(dir, "mall_state.json")); err == nil {
		t.Error("旧 mall_state.json 应被删除")
	}
}

// TestWorkerRollover 验证 Worker 跨天重置清掉了全部"当日已完成"标记
func TestWorkerRollover(t *testing.T) {
	dir := t.TempDir()
	store := NewStateStore(filepath.Join(dir, "task_state.json"))

	// 模拟"昨天已捐 3000"——直接读写内部字段
	store.mu.Lock()
	store.data.Contribute.Date = "2020-01-01"
	store.data.Contribute.ByUID = map[uint32]int{7: 3000}
	store.mu.Unlock()

	yesterday := time.Now().AddDate(0, 0, -1)
	w := &Worker{
		log:            testLogger(),
		stateStore:     store,
		claimedToday:   map[uint64]bool{2150: true, 2151: true},
		claimedDay:     yesterday.Day(),
		checkInKey:     dayKey(yesterday),
		checkInOK:      true,
		mallKey:        dayKey(yesterday),
		hangDoneKey:    dayKey(yesterday),
		gangCheckedDay: dayKey(yesterday),
		gangOK:         true,
		contributeToday: 3000,
		rolloverDay:    dayKey(yesterday),
	}

	w.rolloverIfNeeded()

	if w.checkInKey != 0 || w.checkInOK {
		t.Error("签到标记未重置，新一天将不再签到")
	}
	if w.mallKey != 0 {
		t.Error("商城标记未重置，新一天将不再领取")
	}
	if w.hangDoneKey != 0 {
		t.Error("挂机完成标记未重置，新一天将不再挂机")
	}
	if w.gangCheckedDay != 0 {
		t.Error("战队状态缓存未重置")
	}
	if w.contributeToday != 0 {
		t.Error("当日已捐量未重置")
	}
	if len(w.claimedToday) != 0 {
		t.Error("任务领取记录未重置，新一天任务将无法领取")
	}

	// store 也应跨天作废
	if got := store.ContributeToday(7); got != 0 {
		t.Errorf("store 未跨天清空，实际 %d", got)
	}

	// 同一天重复调用不应重复重置（幂等）
	w.checkInKey = 999
	w.rolloverIfNeeded()
	if w.checkInKey != 999 {
		t.Error("同日内 rollover 不应重复重置")
	}
}