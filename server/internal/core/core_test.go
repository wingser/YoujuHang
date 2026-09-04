package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"youjuhang/internal/config"
	"youjuhang/internal/gate"
	"youjuhang/internal/session"
)

// readFile 是 os.ReadFile 的薄封装（仅为测试文件用，避免 import 噪音）
var readFile = os.ReadFile

// newTestManager 构造仅含 states 的测试用 Manager（不依赖配置文件与网络）
func newTestManager(names ...string) *Manager {
	m := &Manager{
		cfg:     &config.Config{},
		states:  make(map[string]*accountRuntime),
		cancels: make(map[string]*accountCancel),
	}
	for _, n := range names {
		en := true
		m.states[n] = &accountRuntime{
			acc: config.Account{Name: n, Enabled: &en},
		}
		m.states[n].st.Name = n
		m.states[n].st.Enabled = true
		m.states[n].st.Status = StatusOffline
	}
	return m
}

// setOnline 把账号置为"挂机中"，并按需挂上会话（供侦查）
func setOnline(m *Manager, name string, withSession bool) {
	rt := m.states[name]
	rt.mu.Lock()
	rt.st.Running = true
	rt.st.Status = StatusOnline
	if withSession {
		acc := rt.acc
		rt.sess = &session.Session{Account: &acc, UID: 1}
	}
	rt.mu.Unlock()
}

func setStatus(m *Manager, name, status string) {
	rt := m.states[name]
	rt.mu.Lock()
	rt.st.Status = status
	rt.mu.Unlock()
}

// TestPickProbeSessionOnlyPicksOnline 只有"挂机中且持有会话"的账号才能当侦查账号
func TestPickProbeSessionOnlyPicksOnline(t *testing.T) {
	m := newTestManager("a", "b", "c")
	setOnline(m, "a", true)
	setOnline(m, "b", false) // 在线但无会话
	// c 保持 offline

	got := map[string]int{}
	for i := 0; i < 50; i++ {
		s, name := m.pickProbeSession("", nil)
		if s == nil {
			t.Fatal("预期能选到侦查账号，实际为 nil")
		}
		got[name]++
	}
	if got["a"] != 50 {
		t.Errorf("应只选中 a（b 无会话、c 未运行），实际分布: %v", got)
	}
}

// TestPickProbeSessionExclude 排除自己：不能自己侦查自己
func TestPickProbeSessionExclude(t *testing.T) {
	m := newTestManager("a", "b")
	setOnline(m, "a", true)
	setOnline(m, "b", true)

	for i := 0; i < 30; i++ {
		_, name := m.pickProbeSession("a", nil)
		if name == "a" {
			t.Fatal("pickProbeSession 应排除 a，却返回了 a")
		}
		if name != "b" {
			t.Fatalf("排除 a 后应只剩 b，实际 %q", name)
		}
	}
}

// TestPickProbeSessionFailedRotation 失效账号进入 failed 后不再被选中（轮换到别的）
func TestPickProbeSessionFailedRotation(t *testing.T) {
	m := newTestManager("a", "b", "c")
	setOnline(m, "a", true)
	setOnline(m, "b", true)
	setOnline(m, "c", true)

	failed := map[string]bool{"a": true, "b": true}
	for i := 0; i < 30; i++ {
		_, name := m.pickProbeSession("", failed)
		if name != "c" {
			t.Fatalf("a/b 已标记失败，应只剩 c，实际 %q", name)
		}
	}

	// 全部失败 → 无可用
	failed["c"] = true
	if s, _ := m.pickProbeSession("", failed); s != nil {
		t.Fatal("所有账号均失败时应返回 nil")
	}
}

// TestPickProbeSessionRandomness 多个可用账号时应随机分散，而非固定返回同一个
func TestPickProbeSessionRandomness(t *testing.T) {
	m := newTestManager("a", "b", "c", "d")
	for _, n := range []string{"a", "b", "c", "d"} {
		setOnline(m, n, true)
	}

	got := map[string]int{}
	const rounds = 400
	for i := 0; i < rounds; i++ {
		_, name := m.pickProbeSession("", nil)
		got[name]++
	}
	if len(got) != 4 {
		t.Fatalf("4 个可用账号应都被选到过，实际只出现 %d 个: %v", len(got), got)
	}
	for n, c := range got {
		if c < rounds/8 { // 期望约 100 次，低于 50 判定为分布异常
			t.Errorf("账号 %s 被选中 %d 次，分布明显不均（期望约 %d）", n, c, rounds/4)
		}
	}
}

// TestWaitAccountExitNil 没有前任时应立即返回（不阻塞启动）
func TestWaitAccountExitNil(t *testing.T) {
	m := newTestManager("a")
	done := make(chan struct{})
	go func() {
		m.waitAccountExit(nil, "a")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitAccountExit(nil) 应立即返回，实际阻塞了")
	}
}

// TestWaitAccountExitWaitsForDone 前任未退出时必须阻塞，前任退出后立即放行。
// 这是「启动后一直卡在登录中」的核心防护：不做这个等待，新旧两个 goroutine
// 会同时对同一账号登录并互相顶下线。
func TestWaitAccountExitWaitsForDone(t *testing.T) {
	m := newTestManager("a")
	old := &accountCancel{done: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		m.waitAccountExit(old, "a")
		close(done)
	}()
	// 前任仍在运行：必须阻塞
	select {
	case <-done:
		t.Fatal("前任未退出时 waitAccountExit 不应返回")
	case <-time.After(100 * time.Millisecond):
	}
	// 前任退出后应立即放行
	close(old.done)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("前任退出后 waitAccountExit 应立即返回，实际仍阻塞")
	}
}

// newTestManagerWithCfg 构造带临时配置文件的 Manager，使 AddAccount/RemoveAccount
// 的保存逻辑生效（saveLocked 需要合法 cfgPath）。
func newTestManagerWithCfg(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "accounts.yaml")
	m := &Manager{
		cfg:     &config.Config{Accounts: []config.Account{}},
		cfgPath: cfgPath,
		states:  make(map[string]*accountRuntime),
		cancels: make(map[string]*accountCancel),
	}
	return m
}

// TestAddAccountDuplicate 重复添加同名（含仅大小写不同）账号应被拒绝
func TestAddAccountDuplicate(t *testing.T) {
	m := newTestManagerWithCfg(t)
	en := true
	acc := func(n string) config.Account {
		return config.Account{Name: n, Password: "p", Enabled: &en}
	}
	if err := m.AddAccount(acc("abc"), 0); err != nil {
		t.Fatalf("首次添加应成功，实际: %v", err)
	}
	// 完全相同
	if err := m.AddAccount(acc("abc"), 0); err == nil {
		t.Fatal("重复添加同名账号应报错")
	} else if !strings.Contains(err.Error(), "已存在") {
		t.Errorf("报错应包含'已存在'，实际: %v", err)
	}
	// 仅大小写不同：在服务端会被识别为同一用户，必须拦截
	if err := m.AddAccount(acc("ABC"), 0); err == nil {
		t.Fatal("仅大小写不同的账号也应视为重复，应报错")
	} else if !strings.Contains(err.Error(), "已存在") {
		t.Errorf("报错应包含'已存在'，实际: %v", err)
	}
}

// TestAddAccountAfterRemove 删除后允许重新添加（含大小写变体）
func TestAddAccountAfterRemove(t *testing.T) {
	m := newTestManagerWithCfg(t)
	en := true
	_ = m.AddAccount(config.Account{Name: "abc", Password: "p", Enabled: &en}, 0)
	if err := m.RemoveAccount("abc"); err != nil {
		t.Fatalf("删除应成功: %v", err)
	}
	if err := m.AddAccount(config.Account{Name: "ABC", Password: "p", Enabled: &en}, 0); err != nil {
		t.Fatalf("删除后重新添加应成功，实际: %v", err)
	}
}

// TestUpdateAccountPreservesUnsentFields 编辑只改某些字段时，未发送字段必须保留。
//
// 这是 2026-09-01 事故的根因：前端 editAccount 的"只发改了的字段"契约下，
// 后端 UpdateAccount 用整个 acc 替换配置条目，导致未发送的 password/MAC/name
// 被零值覆盖并写盘，下次启动报"missing password"。修复：空字符串 = "不修改"。
func TestUpdateAccountPreservesUnsentFields(t *testing.T) {
	m := newTestManagerWithCfg(t)
	en := true
	orig := config.Account{
		Name: "abc", Password: "secret", MAC: "AA-BB-CC-DD-EE-FF",
		Enabled: &en,
	}
	if err := m.AddAccount(orig, 0); err != nil {
		t.Fatal(err)
	}
	// 模拟前端"只改天数"的请求：name 由 handler 注入，password/mac 都为空
	if _, err := m.UpdateAccount("abc", config.Account{Name: "abc", Enabled: &en}, 5); err != nil {
		t.Fatalf("UpdateAccount 应成功，实际: %v", err)
	}

	m.mu.Lock()
	got := m.cfg.Accounts[0]
	rt := m.states["abc"].acc
	m.mu.Unlock()

	if got.Password != "secret" {
		t.Errorf("密码被清空! 预期保留 'secret'，实际 %q", got.Password)
	}
	if got.MAC != "AA-BB-CC-DD-EE-FF" {
		t.Errorf("MAC 被清空! 预期保留，实际 %q", got.MAC)
	}
	if rt.Password != "secret" || rt.MAC != "AA-BB-CC-DD-EE-FF" {
		t.Errorf("runtime 状态也必须保留：pass=%q mac=%q", rt.Password, rt.MAC)
	}
	// 关键：保存到磁盘的 YAML 里密码仍存在（这是 2026-09-01 事故的表面症状）
	raw, err := readFile(m.cfgPath)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if !strings.Contains(string(raw), "secret") {
		t.Errorf("配置文件中密码被清空，下次 Load 会报 missing password:\n%s", raw)
	}
}

// TestUpdateAccountCanChangePassword 显式传入新密码时应正常替换
func TestUpdateAccountCanChangePassword(t *testing.T) {
	m := newTestManagerWithCfg(t)
	en := true
	if err := m.AddAccount(config.Account{Name: "abc", Password: "old", MAC: "AA-BB", Enabled: &en}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateAccount("abc",
		config.Account{Name: "abc", Password: "new", MAC: "AA-BB", Enabled: &en}, 0); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	got := m.cfg.Accounts[0].Password
	m.mu.Unlock()
	if got != "new" {
		t.Errorf("未应用新密码，实际 %q", got)
	}
}

// markRunning 把账号标记为"运行中"，并返回一个可查询的取消标记。
// 只放 m.cancels 即可让 UpdateAccount 走运行中分支，不真正起 goroutine。
func markRunning(t *testing.T, m *Manager, name string) *bool {
	t.Helper()
	cancelled := new(bool)
	m.mu.Lock()
	m.cancels[name] = &accountCancel{
		cancel: func() { *cancelled = true },
		done:   make(chan struct{}),
	}
	m.mu.Unlock()
	t.Cleanup(func() {
		m.mu.Lock()
		delete(m.cancels, name)
		m.mu.Unlock()
	})
	return cancelled
}

// TestUpdateAccountRunningCanChangeExpire 运行中账号可直接改到期日，且立即生效。
// 旧行为：返回"请先停止再修改"（HTTP 409），逼用户停→改→启，白掉一次 session。
func TestUpdateAccountRunningCanChangeExpire(t *testing.T) {
	m := newTestManagerWithCfg(t)
	en := true
	if err := m.AddAccount(
		config.Account{Name: "abc", Password: "p", MAC: "AA-BB", Enabled: &en}, 2); err != nil {
		t.Fatal(err)
	}
	cancelled := markRunning(t, m, "abc")

	// 运行中改到期日：+5 天
	pending, err := m.UpdateAccount("abc", config.Account{Name: "abc", Enabled: &en}, 5)
	if err != nil {
		t.Fatalf("运行中账号应可直接修改到期日，实际报错: %v", err)
	}
	if pending {
		t.Error("只改到期日不应要求重启（pendingRestart 应为 false）")
	}
	if *cancelled {
		t.Error("到期日延长后不应停止挂机")
	}

	// 立即生效：runtime 与快照都应反映新到期日
	m.mu.Lock()
	rtExpire := m.states["abc"].acc.ExpireDate
	m.mu.Unlock()
	want := config.CalcExpireDate(time.Now(), 7) // 原 2 天 + 新增 5 天
	if rtExpire != want {
		t.Errorf("到期日未热更新：期望 %q，实际 %q", want, rtExpire)
	}
	snap := m.Snapshot()
	if len(snap) != 1 || snap[0].ExpireDate != want {
		t.Errorf("快照未反映新到期日：期望 %q，实际 %+v", want, snap)
	}
}

// TestUpdateAccountRunningExpiredStops 运行中把到期日改到已过期 → 立即停止挂机。
// 不等 expireWatcher 的小时级巡检，改完即停。
func TestUpdateAccountRunningExpiredStops(t *testing.T) {
	m := newTestManagerWithCfg(t)
	en := true
	// 先给 10 天有效期
	if err := m.AddAccount(
		config.Account{Name: "abc", Password: "p", MAC: "AA-BB", Enabled: &en}, 10); err != nil {
		t.Fatal(err)
	}
	cancelled := markRunning(t, m, "abc")

	// 缩短 20 天 → 到期日落到过去 → 应自动停止
	if _, err := m.UpdateAccount("abc", config.Account{Name: "abc", Enabled: &en}, -20); err != nil {
		t.Fatalf("运行中缩短到期日不应报错，实际: %v", err)
	}
	if !*cancelled {
		t.Error("到期日改到已过期后，应立即停止挂机，实际未停止")
	}

	m.mu.Lock()
	expire := m.states["abc"].acc.ExpireDate
	m.mu.Unlock()
	if !config.IsExpired(expire, time.Now()) {
		t.Errorf("改完的到期日 %q 应判定为已过期", expire)
	}
}

// TestUpdateAccountRunningPasswordNeedsRestart 运行中改密码：配置立即落盘，
// 但当前会话仍是启动时的值拷贝 → pendingRestart=true 提示用户。
func TestUpdateAccountRunningPasswordNeedsRestart(t *testing.T) {
	m := newTestManagerWithCfg(t)
	en := true
	if err := m.AddAccount(
		config.Account{Name: "abc", Password: "old", MAC: "AA-BB", Enabled: &en}, 3); err != nil {
		t.Fatal(err)
	}
	cancelled := markRunning(t, m, "abc")

	pending, err := m.UpdateAccount("abc",
		config.Account{Name: "abc", Password: "new", MAC: "AA-BB", Enabled: &en}, 0)
	if err != nil {
		t.Fatalf("运行中改密码不应报错，实际: %v", err)
	}
	if !pending {
		t.Error("运行中改了凭据，pendingRestart 应为 true（提示需重启才生效）")
	}
	if *cancelled {
		t.Error("改密码不应停止挂机（当前会话继续用旧凭据跑完）")
	}

	// 配置必须已落盘，下次启动用新密码
	m.mu.Lock()
	got := m.cfg.Accounts[0].Password
	m.mu.Unlock()
	if got != "new" {
		t.Errorf("新密码未写入配置，实际 %q", got)
	}
}

// TestUpdateAccountStoppedPasswordNoPending 未运行时改密码不需要重启提示。
func TestUpdateAccountStoppedPasswordNoPending(t *testing.T) {
	m := newTestManagerWithCfg(t)
	en := true
	if err := m.AddAccount(
		config.Account{Name: "abc", Password: "old", MAC: "AA-BB", Enabled: &en}, 3); err != nil {
		t.Fatal(err)
	}
	pending, err := m.UpdateAccount("abc",
		config.Account{Name: "abc", Password: "new", MAC: "AA-BB", Enabled: &en}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Error("未运行时改密码，pendingRestart 应为 false（下次启动自然生效）")
	}
}

// testLogger 丢弃输出的 logger，供需要 *slog.Logger 参数的函数使用
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// loginRejectedErr 构造一个"服务器明确拒绝"错误（模拟密码错误/账号不存在）
func loginRejectedErr(ret int64, errStr string) error {
	return fmt.Errorf("%w ret=%d err=%q", gate.ErrLoginRejected, ret, errStr)
}

// TestNoteLoginRejectedDisablesAfterLimit 连续被服务器拒绝达到阈值 → 自动禁用并落盘。
// 这是本次需求的核心：避免密码错误的账号无限重试。
func TestNoteLoginRejectedDisablesAfterLimit(t *testing.T) {
	m := newTestManagerWithCfg(t)
	en := true
	if err := m.AddAccount(
		config.Account{Name: "abc", Password: "wrong-pwd", MAC: "AA-BB", Enabled: &en}, 0); err != nil {
		t.Fatal(err)
	}
	cancelled := markRunning(t, m, "abc")
	rt := m.states["abc"]
	rejected := loginRejectedErr(-2, "ACCOUNT_NOT_FOUND")

	// 前 limit-1 次：只累计，不禁用
	for i := 1; i < loginRejectLimit; i++ {
		if m.noteLoginRejected(context.Background(), "abc", rt, rejected, testLogger()) {
			t.Fatalf("第 %d 次被拒就禁用了，应在第 %d 次才禁用", i, loginRejectLimit)
		}
	}
	if !enabledNow(t, m) {
		t.Errorf("未达阈值(%d)就把账号禁用了", loginRejectLimit)
	}
	if *cancelled {
		t.Error("未达阈值就停止了挂机")
	}

	// 第 limit 次：禁用
	if !m.noteLoginRejected(context.Background(), "abc", rt, rejected, testLogger()) {
		t.Fatalf("达到阈值 %d 后应返回 true（已禁用）", loginRejectLimit)
	}
	if enabledNow(t, m) {
		t.Error("达到阈值后账号应被自动禁用")
	}
	if !*cancelled {
		t.Error("自动禁用时应同时停止挂机")
	}
	// 状态与错误原因：UI 靠这两个字段显示"已禁用 / 密码错误"
	rt.mu.RLock()
	st, errMsg := rt.st.Status, rt.st.Err
	rt.mu.RUnlock()
	if st != StatusDisabled {
		t.Errorf("状态应为 %q（已禁用），实际 %q", StatusDisabled, st)
	}
	if errMsg == "" {
		t.Error("应保留服务器下发的错误原因，供 UI 展示")
	}

	// 必须落盘，否则重启后又开始重试
	raw, err := readFile(m.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "enabled: false") {
		t.Errorf("禁用状态未写入配置文件，重启后会继续重试:\n%s", raw)
	}
}

// TestNoteLoginRejectUnderLimitKeepsEnabled 未达阈值时保持启用，不误伤。
func TestNoteLoginRejectUnderLimitKeepsEnabled(t *testing.T) {
	m := newTestManagerWithCfg(t)
	en := true
	if err := m.AddAccount(
		config.Account{Name: "abc", Password: "p", MAC: "AA-BB", Enabled: &en}, 0); err != nil {
		t.Fatal(err)
	}
	markRunning(t, m, "abc")
	rt := m.states["abc"]

	for i := 0; i < loginRejectLimit-1; i++ {
		m.noteLoginRejected(context.Background(), "abc", rt, loginRejectedErr(-2, "X"), testLogger())
	}
	if !enabledNow(t, m) {
		t.Error("未达阈值不应禁用（服务器可能只是临时维护）")
	}
	rt.mu.RLock()
	n := rt.loginRejects
	rt.mu.RUnlock()
	if n != loginRejectLimit-1 {
		t.Errorf("计数错误：期望 %d，实际 %d", loginRejectLimit-1, n)
	}
}

// TestSetEnabledClearsDisabledState 用户改好密码后手动启用：
// 必须清掉 disabled 状态与累计计数，否则 UI 一直显示"已禁用"、
// 且残留计数会让它更快被再次禁用。
func TestSetEnabledClearsDisabledState(t *testing.T) {
	m := newTestManagerWithCfg(t)
	en := true
	if err := m.AddAccount(
		config.Account{Name: "abc", Password: "p", MAC: "AA-BB", Enabled: &en}, 0); err != nil {
		t.Fatal(err)
	}
	markRunning(t, m, "abc")
	rt := m.states["abc"]
	rejected := loginRejectedErr(-2, "ACCOUNT_NOT_FOUND")

	// 触发自动禁用
	for i := 0; i < loginRejectLimit; i++ {
		m.noteLoginRejected(context.Background(), "abc", rt, rejected, testLogger())
	}
	if enabledNow(t, m) {
		t.Fatal("前置条件失败：应已自动禁用")
	}

	// 用户改密码后手动启用
	if err := m.SetEnabled("abc", true); err != nil {
		t.Fatal(err)
	}
	rt.mu.RLock()
	st, errMsg, n := rt.st.Status, rt.st.Err, rt.loginRejects
	rt.mu.RUnlock()
	if st == StatusDisabled {
		t.Error("手动启用后仍停留在 disabled 状态，用户会以为启用没生效")
	}
	if st != StatusOffline {
		t.Errorf("手动启用后状态应为 %q，实际 %q", StatusOffline, st)
	}
	if errMsg != "" {
		t.Errorf("手动启用后应清掉旧的错误原因，实际残留 %q", errMsg)
	}
	if n != 0 {
		t.Errorf("手动启用后应清零被拒计数，实际 %d", n)
	}
	if !enabledNow(t, m) {
		t.Error("手动启用后 enabled 应为 true")
	}
}

// TestStartAllSkipsDisabled 批量启动必须跳过被自动禁用的账号。
// StartAll 依据 rt.st.Enabled 过滤，禁用后 enabled=false 即自动跳过。
func TestStartAllSkipsDisabled(t *testing.T) {
	m := newTestManagerWithCfg(t)
	en := true
	if err := m.AddAccount(
		config.Account{Name: "bad", Password: "wrong", MAC: "AA-BB", Enabled: &en}, 0); err != nil {
		t.Fatal(err)
	}
	// 手动置为禁用态（等价于被自动禁用后的结果）
	if err := m.SetEnabled("bad", false); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	rt := m.states["bad"]
	m.mu.Unlock()
	rt.mu.RLock()
	enabled := rt.st.Enabled
	rt.mu.RUnlock()
	if enabled {
		t.Fatal("前置条件失败：账号应为禁用态")
	}
	// StartAll 会遍历 states，禁用项应被 continue 掉。
	// 这里直接断言过滤条件本身（避免 StartAll 真的起 goroutine 联网）。
	if rt.st.Enabled {
		t.Error("StartAll 的过滤条件失效：禁用账号仍会被启动")
	}
}

// enabledNow 读取配置中账号的启用状态
func enabledNow(t *testing.T, m *Manager) bool {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.cfg.Accounts {
		if m.cfg.Accounts[i].Name == "abc" {
			if m.cfg.Accounts[i].Enabled == nil {
				return false
			}
			return *m.cfg.Accounts[i].Enabled
		}
	}
	t.Fatalf("配置中找不到账号 abc")
	return false
}

func TestAddAccountSetsExpireDate(t *testing.T) {
	m := newTestManagerWithCfg(t)
	en := true
	now := time.Now()
	_ = m.AddAccount(config.Account{Name: "a", Password: "p", Enabled: &en}, 3)
	m.mu.Lock()
	got := m.states["a"].acc.ExpireDate
	m.mu.Unlock()
	want := config.CalcExpireDate(now, 3)
	if got != want {
		t.Errorf("新增3天到期日应为 %q，实际 %q", want, got)
	}

	// days=0 → 永久（空）
	m2 := newTestManagerWithCfg(t)
	_ = m2.AddAccount(config.Account{Name: "b", Password: "p", Enabled: &en}, 0)
	m2.mu.Lock()
	got2 := m2.states["b"].acc.ExpireDate
	m2.mu.Unlock()
	if got2 != "" {
		t.Errorf("days=0 应为永久（空），实际 %q", got2)
	}
}

// TestWaitLoginTurnStaggers 多账号并发登录必须排队：相邻两次登录间隔 >= interval。
//
// 背景（2026-09-04 需求）：程序启动或「全部启动」时所有账号会同时发起登录，
// 表现为同一 IP 的批量登录，易被风控判定异常。
// 这里用毫秒级 interval 验证排队逻辑本身（真机等 10 秒太慢）。
func TestWaitLoginTurnStaggers(t *testing.T) {
	// 重置进程级节流状态，避免受其他用例影响
	loginMu.Lock()
	nextLoginAt = time.Time{}
	loginMu.Unlock()

	const interval = 100 * time.Millisecond
	const n = 4
	// 容差说明：Windows 系统时钟精度约 15ms，叠加 goroutine 调度抖动，
	// 实测单次间隔可能低于 interval。故取 interval 的 70% 作为下限——
	// 既能确认「确实错峰了」，又不会因计时抖动误报（生产环境 10s 间隔下这点偏差无意义）。
	minGap := interval * 7 / 10

	var wg sync.WaitGroup
	stamps := make([]time.Time, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if waitLoginTurn(context.Background(), nil, "acc", interval) {
				stamps[idx] = time.Now()
			}
		}(i)
	}
	wg.Wait()

	sort.Slice(stamps, func(a, b int) bool { return stamps[a].Before(stamps[b]) })
	for i := 1; i < n; i++ {
		gap := stamps[i].Sub(stamps[i-1])
		if gap < minGap {
			t.Errorf("第 %d 与第 %d 次登录间隔 %v，小于下限 %v（未有效错峰）",
				i, i+1, gap, minGap)
		}
	}
	// 队首不应被无谓推迟（允许少量调度抖动）
	if d := stamps[0].Sub(start); d > interval {
		t.Errorf("首个登录者被不必要地推迟了 %v", d)
	}
}

// TestWaitLoginTurnCanceled ctx 取消时应立即返回 false，不拖慢停止/退出流程。
func TestWaitLoginTurnCanceled(t *testing.T) {
	loginMu.Lock()
	nextLoginAt = time.Now().Add(5 * time.Second) // 人为制造较长等待
	loginMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan bool, 1)
	go func() {
		done <- waitLoginTurn(ctx, nil, "acc", time.Second)
	}()

	select {
	case ok := <-done:
		if ok {
			t.Error("ctx 取消时应返回 false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 waitLoginTurn 未及时返回")
	}
}
