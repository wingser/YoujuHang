package biz

import (
	"context"
	"testing"
	"time"

	"youjuhang/internal/config"
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
