package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mustDate(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

// mustDateTime 解析带时刻的时间（用于验证"当天任意时刻都不算过期"）
func mustDateTime(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02T15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

// TestCalcExpireDate 新增账号：从今天起 N 天，<=0 为永久
func TestCalcExpireDate(t *testing.T) {
	cases := []struct {
		today    string
		days     int
		expected string
	}{
		{"2026-08-31", 3, "2026-09-03"},   // 跨月
		{"2026-08-31", 1, "2026-09-01"},   // 跨月的最小跨度
		{"2026-12-30", 3, "2027-01-02"},   // 跨年
		{"2028-02-28", 1, "2028-02-29"},   // 闰年 2 月
		{"2027-02-28", 1, "2027-03-01"},   // 平年 2 月
		{"2026-08-31", 0, ""},             // 0 天 = 永久
		{"2026-08-31", -5, ""},            // 负数 = 永久
	}
	for _, c := range cases {
		got := CalcExpireDate(mustDate(c.today), c.days)
		if got != c.expected {
			t.Errorf("CalcExpireDate(%s, %d) = %q, 期望 %q",
				c.today, c.days, got, c.expected)
		}
	}
}

// TestIsExpired 到期判定：到期日当天仍可挂机，次日才过期；空=永久
func TestIsExpired(t *testing.T) {
	cases := []struct {
		expire   string
		today    string
		expected bool
		note     string
	}{
		{"2026-09-03", "2026-08-31", false, "未到"},
		{"2026-09-03", "2026-09-03", false, "到期日当天仍可挂机"},
		{"2026-09-03", "2026-09-04", true, "次日过期"},
		{"2026-09-03", "2026-10-01", true, "远超期"},
		{"", "2027-01-01", false, "空=永久"},
		{"bad-format", "2026-09-04", false, "格式非法按永久处理，避免误停"},
		{"2026-09-03", "2026-09-03T23:59", false, "当天任意时刻都不过期"},
	}
	for _, c := range cases {
		var now time.Time
		if len(c.today) > 10 { // 带时刻的用例
			now = mustDateTime(c.today)
		} else {
			now = mustDate(c.today)
		}
		got := IsExpired(c.expire, now)
		if got != c.expected {
			t.Errorf("IsExpired(%q, %s) = %v, 期望 %v（%s）",
				c.expire, c.today, got, c.expected, c.note)
		}
	}
}

// TestExtendExpireDate 编辑调整天数（2026-09-02 规则）：
// 基数取「原到期日」还是「今天」，取决于账号当前是否已过期。
//
//	未过期（含当天）：days>0 原到期日+days ；days==0 不变 ；days<0 原到期日+days
//	已过期          ：days>0 今天+days      ；days==0 今天  ；days<0 原到期日+days
//	永久/格式非法   ：days>0 今天+days      ；days<=0 原样返回
func TestExtendExpireDate(t *testing.T) {
	cases := []struct {
		old      string
		today    string
		days     int
		expected string
		note     string
	}{
		// --- 未过期（含到期日当天）：一律以原到期日为基数 ---
		{"2026-09-03", "2026-09-02", 30, "2026-10-03", "未过期+正数→在原到期日上累加"},
		{"2026-09-03", "2026-09-02", 0, "2026-09-03", "未过期+0→保持不变"},
		{"2026-09-03", "2026-09-02", -3, "2026-08-31", "未过期+负数→在原到期日上减少"},
		{"2026-09-02", "2026-09-02", 5, "2026-09-07", "到期日当天+正数→基于当天累加"},
		{"2026-09-02", "2026-09-02", 0, "2026-09-02", "到期日当天+0→不变（当天仍可挂机）"},
		{"2026-09-03", "2026-08-31", -10, "2026-08-24", "未过期+负数减到今天之前→随即过期"},
		{"2026-12-25", "2026-12-01", 30, "2027-01-24", "跨年累加"},
		{"2027-01-02", "2026-12-30", -5, "2026-12-28", "跨年减少"},
		{"2028-03-01", "2028-02-28", -1, "2028-02-29", "闰年 2 月"},
		{"2027-03-01", "2027-02-28", -1, "2027-02-28", "平年 2 月"},

		// --- 已过期：正数/0 以今天为基数，负数仍以原到期日为基数 ---
		{"2026-08-26", "2026-09-02", 1, "2026-09-03", "一周前过期+1天→明天（用户场景）"},
		{"2026-09-01", "2026-09-02", 1, "2026-09-03", "昨天过期+1天→明天"},
		{"2026-08-26", "2026-09-02", 30, "2026-10-02", "已过期+正数→从今天起算"},
		{"2026-08-26", "2026-09-02", 0, "2026-09-02", "已过期+0→拉回今天（今天仍可挂机）"},
		{"2026-09-01", "2026-09-02", 0, "2026-09-02", "昨天过期+0→改为今天"},
		{"2026-08-26", "2026-09-02", -3, "2026-08-23", "已过期+负数→仍基于原到期日减少"},

		// --- 永久/格式非法：正数从今天起算，0 与负数原样返回 ---
		{"", "2026-09-02", 30, "2026-10-02", "永久+正数→从今天起算"},
		{"", "2026-09-02", 0, "", "永久+0→保持永久"},
		{"", "2026-09-02", -30, "", "永久+负数→无法减少，保持永久"},
		{"bad", "2026-09-02", 10, "2026-09-12", "非法格式+正数→从今天起算"},
		{"bad", "2026-09-02", 0, "bad", "非法格式+0→原样保留"},
		{"bad", "2026-09-02", -10, "bad", "非法格式+负数→原样保留"},
	}
	for _, c := range cases {
		got := ExtendExpireDate(c.old, mustDate(c.today), c.days)
		if got != c.expected {
			t.Errorf("ExtendExpireDate(%q, %s, %d) = %q, 期望 %q（%s）",
				c.old, c.today, c.days, got, c.expected, c.note)
		}
	}
}

// TestExtendThenShrinkRoundTrip 延长后再缩短，应回到原到期日（可逆）
func TestExtendThenShrinkRoundTrip(t *testing.T) {
	today := mustDate("2026-08-31")
	start := "2026-09-03"

	exp := ExtendExpireDate(start, today, 30)
	if exp != "2026-10-03" {
		t.Fatalf("延长 30 天应为 2026-10-03，实际 %q", exp)
	}
	exp = ExtendExpireDate(exp, today, -30)
	if exp != start {
		t.Fatalf("再缩短 30 天应回到 %q，实际 %q", start, exp)
	}
}

// TestAddThenExtendRoundTrip 新增 3 天后连续续期两次，到期日应单调累加
func TestAddThenExtendRoundTrip(t *testing.T) {
	today := mustDate("2026-08-31")

	exp := CalcExpireDate(today, 3)
	if exp != "2026-09-03" {
		t.Fatalf("新增 3 天应为 2026-09-03，实际 %q", exp)
	}
	exp = ExtendExpireDate(exp, today, 10)
	if exp != "2026-09-13" {
		t.Fatalf("续期 10 天应为 2026-09-13，实际 %q", exp)
	}
	exp = ExtendExpireDate(exp, today, 20)
	if exp != "2026-10-03" {
		t.Fatalf("再续期 20 天应为 2026-10-03，实际 %q", exp)
	}
}

// TestExtendExpiredAccountBecomesUsable 已过期账号续期后必须重新变为「可用」。
// 这是 2026-09-02 规则调整的核心意图：若仍从过期日累加，加完还是过去日期，
// 账号依旧不可用（旧行为），因此已过期时改以今天为基数。
func TestExtendExpiredAccountBecomesUsable(t *testing.T) {
	today := mustDate("2026-09-02")
	old := "2026-08-26" // 上周已过期
	if !IsExpired(old, today) {
		t.Fatalf("前置条件不成立：%s 相对今天应已过期", old)
	}

	// 填 1 天 → 到期日为明天，且不再过期
	got := ExtendExpireDate(old, today, 1)
	if got != "2026-09-03" {
		t.Fatalf("已过期账号 +1 天应为明天 2026-09-03，实际 %q", got)
	}
	if IsExpired(got, today) {
		t.Errorf("续期 1 天后仍被判为过期：%q", got)
	}

	// 填 0 → 拉回今天，到期日当天仍可挂机（不算过期）
	got0 := ExtendExpireDate(old, today, 0)
	if got0 != "2026-09-02" {
		t.Errorf("已过期账号填 0 应改为今天 2026-09-02，实际 %q", got0)
	}
	if IsExpired(got0, today) {
		t.Errorf("填 0 拉回今天后仍被判为过期：%q", got0)
	}
}

// TestLoadMissingBoolFieldsGetDefaults 旧配置文件缺少新 bool 字段时必须取默认值。
//
// 回归背景（2026-09-09 线上事故）：Load 曾直接 Unmarshal 到零值结构，
// 旧文件里没有的 avatar_enabled 被解析成 false → 功能静默禁用，
// 且日志一条记录都没有（maybeDressAvatar 第一行就 return），极难排查。
func TestLoadMissingBoolFieldsGetDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.yaml")
	// 模拟旧版真实配置：只有账号与地址，不含任何 bool 开关
	content := `web_addr: 127.0.0.1:29090
accounts:
    - name: someone
      password: secret
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for name, got := range map[string]bool{
		"check_in_enabled":   cfg.CheckInEnabled,
		"auto_claim":         cfg.AutoClaim,
		"mall_enabled":       cfg.MallEnabled,
		"avatar_enabled":     cfg.AvatarEnabled,
		"room_hang_enabled":  cfg.RoomHangEnabled,
		"contribute_enabled": cfg.ContributeEnabled,
	} {
		if !got {
			t.Errorf("配置文件未写 %s 时应默认 true，实际 false（新功能被静默禁用）", name)
		}
	}
	// int 类默认值同样必须生效（Load 以 DefaultConfig 打底）
	if cfg.AvatarRenewBeforeDays != 1 {
		t.Errorf("avatar_renew_before_days 未配置时应默认 1（剩余≤1天提前换），实际 %d",
			cfg.AvatarRenewBeforeDays)
	}
}
