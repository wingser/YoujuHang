package biz

import (
	"context"
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
