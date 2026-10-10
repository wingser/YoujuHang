package biz

import (
	"context"
	"strings"
	"testing"
	"time"

	"youjuhang/internal/config"
	"youjuhang/internal/mall"
)

// TestStartAvatarCheckSkipConditions 头像异步检查的启动条件（2026-09-10 并发防护）。
//
// 异步后出现过两类状态不一致风险，这里用测试锁住防护：
//  1. 跨天 + 慢请求：goroutine 仍在跑时 rolloverIfNeeded 清零 avatarKey，
//     主循环会误判"今天没查过"再起一个 → 两个 goroutine 并发佩戴，状态抖动；
//  2. 主循环每 10 分钟一轮，若不判断会重复起 goroutine。
//
// 以下三种场景都不应启动检查（不置 avatarChecking），因此不会发起真实网络请求。
func TestStartAvatarCheckSkipConditions(t *testing.T) {
	// 1. 功能未启用
	w1 := &Worker{cfg: &config.Config{AvatarEnabled: false}}
	w1.startAvatarCheck(context.Background())
	if w1.avatarChecking {
		t.Error("功能禁用时不应启动头像检查")
	}

	// 2. 当天已检查过（avatarKey == 今天）
	w2 := &Worker{cfg: &config.Config{AvatarEnabled: true}, avatarKey: dayKey(time.Now())}
	w2.startAvatarCheck(context.Background())
	if w2.avatarChecking {
		t.Error("当天已检查过不应重复启动")
	}

	// 3. 已有检查在跑（avatarChecking == true）：必须保持，不得再起一个。
	//    注意这里**不能**断言它变成 false——标志由 goroutine 结束时清理，
	//    本用例不启 goroutine，标志应原样保持 true。
	w3 := &Worker{cfg: &config.Config{AvatarEnabled: true}, avatarChecking: true}
	w3.startAvatarCheck(context.Background())
	if !w3.avatarChecking {
		t.Error("检查进行中时标志应保持 true（不得被重置后再起一个）")
	}
}

// TestEarlyRenewWorth 提前更换的判定（2026-10-03 用户要求）：
// 候选必须**有效期更长**且**加成不低于当前**，才值得提前换；
// 否则宁可把当前头像用到过期，避免为了续期反而降了加成。
func TestEarlyRenewWorth(t *testing.T) {
	// 当前：1.4 倍，还剩 1 天（进入提前更换窗口）
	worn := &mall.DressItem{GoodID: 58, Title: "铁血士兵", Exp: 1.4, LeftDays: 1}

	cases := []struct {
		name string
		best *mall.DressItem
		want bool
		note string
	}{
		{"加成更高且有效期更长", &mall.DressItem{GoodID: 127, Exp: 1.5, LeftDays: 25}, true, "加成升级"},
		{"加成相同且有效期更长", &mall.DressItem{GoodID: 127, Exp: 1.4, LeftDays: 25}, true, "同等加成续期"},
		{"加成更低（核心场景：不换）", &mall.DressItem{GoodID: 200, Exp: 1.2, LeftDays: 30}, false,
			"不能为了 30 天的 1.2 倍放弃还剩 1 天的 1.4 倍"},
		{"加成更高但有效期更短", &mall.DressItem{GoodID: 201, Exp: 1.5, LeftDays: 1}, false,
			"没有延长期限的意义"},
		{"有效期相同", &mall.DressItem{GoodID: 202, Exp: 1.4, LeftDays: 1}, false, ""},
		{"候选为永久", &mall.DressItem{GoodID: 203, Exp: 1.4, LeftDays: mall.LeftDaysForever}, true, ""},
		{"候选为空", nil, false, ""},
	}
	for _, c := range cases {
		if got := earlyRenewWorth(worn, c.best); got != c.want {
			t.Errorf("%s: earlyRenewWorth() = %v, 期望 %v（%s）", c.name, got, c.want, c.note)
		}
	}
	if earlyRenewWorth(nil, &mall.DressItem{Exp: 2, LeftDays: 9}) {
		t.Error("当前头像为 nil 时应返回 false")
	}
}

// TestAvatarDueRetryBackoff 瞬态失败退避必须拦住下一轮检查（2026-10-10 事故修复）。
//
// 事故：主循环只在首轮调用 startAvatarCheck，avatarKey 写成当天后本进程内不再重查；
// 若首轮检查恰好遇到商城超时，头像行会空白到下次重登（实测 chouyoku/wingser 空白两天）。
// 修法：瞬态失败回滚当日标记 + 退避重试，退避窗口内不得发起检查。
func TestAvatarDueRetryBackoff(t *testing.T) {
	now := time.Now()

	// 1. 退避窗口内：不起，且不得置位 avatarChecking
	w1 := &Worker{cfg: &config.Config{AvatarEnabled: true}, avatarRetryAt: now.Add(time.Minute)}
	if w1.avatarDue(now) {
		t.Error("退避窗口内不应发起头像检查")
	}
	if w1.avatarChecking {
		t.Error("被退避拦住时不得置位 avatarChecking")
	}

	// 2. 退避已过：放行，并置位 avatarChecking（由检查结束时清零）
	w2 := &Worker{cfg: &config.Config{AvatarEnabled: true}, avatarRetryAt: now.Add(-time.Second)}
	if !w2.avatarDue(now) {
		t.Error("退避已过应发起头像检查")
	}
	if !w2.avatarChecking {
		t.Error("放行时应置位 avatarChecking")
	}

	// 3. 未设置退避（零值）：视为不退避
	w3 := &Worker{cfg: &config.Config{AvatarEnabled: true}}
	if !w3.avatarDue(now) {
		t.Error("未设置退避时不应被拦住")
	}

	// 4. 当天已有结论：不起（这是把「每天一次」压住的关键条件）
	w4 := &Worker{cfg: &config.Config{AvatarEnabled: true}, avatarKey: dayKey(now)}
	if w4.avatarDue(now) {
		t.Error("当天已处理过不应重复发起")
	}
}

// TestAvatarTransientRollsBackAndBacksOff 瞬态失败必须：回滚当日标记、退避递增且封顶、
// 并写入 UI 文案（文案为空时前端整行不渲染，用户看到的是"这一行不见了"）。
func TestAvatarTransientRollsBackAndBacksOff(t *testing.T) {
	now := time.Now()
	w := &Worker{cfg: &config.Config{AvatarEnabled: true}, log: testLogger()}
	w.avatarKey = dayKey(now) // 模拟"今天已检查过"

	w.avatarTransient("个人空间获取失败")

	if w.avatarKey != 0 {
		t.Errorf("瞬态失败后应回滚 avatarKey 为 0，实际 %d", w.avatarKey)
	}
	if w.avatarRetryBackoff != avatarRetryMinBackoff {
		t.Errorf("首次退避应为 %v，实际 %v", avatarRetryMinBackoff, w.avatarRetryBackoff)
	}
	if left := time.Until(w.avatarRetryAt); left <= 0 || left > avatarRetryMinBackoff+time.Second {
		t.Errorf("avatarRetryAt 应落在未来 %v 内，实际剩余 %v", avatarRetryMinBackoff, left)
	}
	if w.avatarMsg == "" || !strings.Contains(w.avatarMsg, "个人空间获取失败") {
		t.Errorf("瞬态失败必须写入 UI 文案，实际 %q", w.avatarMsg)
	}

	// 持续失败 → 退避翻倍
	w.avatarTransient("个人空间获取失败")
	if w.avatarRetryBackoff != 2*avatarRetryMinBackoff {
		t.Errorf("第二次退避应为 %v，实际 %v", 2*avatarRetryMinBackoff, w.avatarRetryBackoff)
	}

	// 再失败多次 → 封顶，不得无限增长
	for i := 0; i < 12; i++ {
		w.avatarTransient("个人空间获取失败")
	}
	if w.avatarRetryBackoff != avatarRetryMaxBackoff {
		t.Errorf("退避必须封顶在 %v，实际 %v", avatarRetryMaxBackoff, w.avatarRetryBackoff)
	}
}

// TestRolloverClearsAvatarRetry 跨天必须清掉退避状态：
// 否则昨天的退避会把新的一天第一次检查一起挡住（最长 2 小时）。
func TestRolloverClearsAvatarRetry(t *testing.T) {
	yesterday := dayKey(time.Now().AddDate(0, 0, -1))
	w := &Worker{cfg: &config.Config{AvatarEnabled: true}, log: testLogger()}
	w.rolloverDay = yesterday
	w.avatarKey = yesterday
	w.avatarRetryAt = time.Now().Add(90 * time.Minute)
	w.avatarRetryBackoff = 45 * time.Minute

	w.rolloverIfNeeded()

	if w.avatarKey != 0 {
		t.Errorf("跨天后 avatarKey 应清零，实际 %d", w.avatarKey)
	}
	if !w.avatarRetryAt.IsZero() || w.avatarRetryBackoff != 0 {
		t.Errorf("跨天后退避状态应清零，实际 retryAt=%v backoff=%v",
			w.avatarRetryAt, w.avatarRetryBackoff)
	}
	// 清零后应立刻允许发起检查：这才是「每天重新检查」真正生效的地方
	if !w.avatarDue(time.Now()) {
		t.Error("跨天清零后应允许立即发起头像检查")
	}
}
