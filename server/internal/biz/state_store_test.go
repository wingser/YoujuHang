package biz

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// pinNow 把 nowFn 固定为给定日期（本地时区），使 StateStore 的"当日捐献"判定不再随
// 测试执行日期漂移。
//
// 背景：本文件多个 load 回归测试用固定日期 2026-09-01 的 fixture，断言 ContributeToday 能
// 读回已捐量（如 1300）。但 ContributeToday 内部会按"是否新的一天"调用
// resetContributeIfNewDayLocked —— 若今天已跨过 fixture 日期，ByUID 会被清空、返回 0，
// 导致 load 测试误报失败（2026-09-02 实测：9-1 写的 fixture 在 9-2 跑全红）。
// 这些测试要验证的是"load 是否保真"，而非"跨天是否清空"（后者由 TestStateStoreCrossDay 覆盖），
// 因此这里把 nowFn 钉到 fixture 当天，隔离两类逻辑。测试结束自动还原。
func pinNow(t *testing.T, date string) {
	t.Helper()
	old := nowFn
	d, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		t.Fatalf("pinNow: 日期解析失败 %q: %v", date, err)
	}
	nowFn = func() time.Time { return d }
	t.Cleanup(func() { nowFn = old })
}

// TestStateStoreLegacyFormatInNewFileName 旧格式内容 + 新文件名必须被正确识别。
//
// 真实事故（2026-09-01）：用户把 contribute_state.json **改名**成 task_state.json，
// 内容是旧格式 {"date":..., "by_uid":{...}}。
// 而 Go 的 json.Unmarshal 会**忽略不匹配的顶层字段并成功返回**，
// 导致 s.data.Contribute 全为零值 → ContributeToday 返回 0 →
// **程序认为今天还没捐过 → 重复捐献**（正是用户最担心的事）。
func TestStateStoreLegacyFormatInNewFileName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task_state.json")

	// 模拟用户操作：把旧的 contribute_state.json 改名成 task_state.json
	pinNow(t, "2026-09-01")
	legacy := `{"date":"2026-09-01","by_uid":{"5571561":1300,"6017780":3000}}`
	if err := os.WriteFile(path, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}

	s := NewStateStore(path)

	// 关键断言：必须读到旧格式里的值，而不是 0
	if got := s.ContributeToday(5571561); got != 1300 {
		t.Errorf("ContributeToday(5571561) = %d, want 1300 —— 旧格式内容被当成空，会导致重复捐献!", got)
	}
	if got := s.ContributeToday(6017780); got != 3000 {
		t.Errorf("ContributeToday(6017780) = %d, want 3000 —— 已捐满的账号会被重复捐献!", got)
	}
}

// TestStateStoreNewFormat 新格式正常加载
func TestStateStoreNewFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task_state.json")

	newFmt := `{
  "contribute": {"date": "2026-09-01", "by_uid": {"5571561": 1300}},
  "mall": {"by_uid": {"6017780": {"claimed_month": 202609}}}
}`
	if err := os.WriteFile(path, []byte(newFmt), 0644); err != nil {
		t.Fatal(err)
	}

	pinNow(t, "2026-09-01")
	s := NewStateStore(path)
	if got := s.ContributeToday(5571561); got != 1300 {
		t.Errorf("ContributeToday(5571561) = %d, want 1300", got)
	}
	if !s.MallIsClaimed(6017780, 202609) {
		t.Error("MallIsClaimed 应为 true")
	}
}

// TestStateStoreMallMarkedUnavailablePersists 标记不可领后重启仍记得
func TestStateStoreMallMarkedUnavailablePersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task_state.json")

	s := NewStateStore(path)
	if err := s.MallMarkUnavailable(9068537, 202609, "账号尚未绑定微信"); err != nil {
		t.Fatal(err)
	}

	// 模拟重启
	s2 := NewStateStore(path)
	if !s2.MallIsUnavailable(9068537, 202609) {
		t.Error("重启后应记住本月不可领取")
	}
	if got := s2.MallLastInfo(9068537); got != "账号尚未绑定微信" {
		t.Errorf("MallLastInfo = %q, want %q", got, "账号尚未绑定微信")
	}
}

// TestMallAlreadyClaimedRealSamples 用生产环境实测样本验证（2026-09-01 抓到）
func TestMallAlreadyClaimedRealSamples(t *testing.T) {
	// 生产实测：chouyoku / wingser 重复领取时返回
	if !mallAlreadyClaimed("您已经领取过此礼包") {
		t.Error("实测样本「您已经领取过此礼包」应识别为已领取")
	}
	// 未绑定微信的文案不应被误判为已领取
	if mallAlreadyClaimed("账号尚未绑定微信") {
		t.Error("「账号尚未绑定微信」不应识别为已领取")
	}
}

// TestMallUnavailableRealSamples 用生产环境实测样本验证（2026-09-01 抓到）
func TestMallUnavailableRealSamples(t *testing.T) {
	// 生产实测：youjugua（未绑定微信）返回
	if !mallUnavailable("账号尚未绑定微信") {
		t.Error("实测样本「账号尚未绑定微信」应识别为不可领取")
	}
	// 已领取过的文案不应被误判为不可领取
	if mallUnavailable("您已经领取过此礼包") {
		t.Error("「您已经领取过此礼包」不应识别为不可领取")
	}
}
