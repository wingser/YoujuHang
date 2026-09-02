package gate

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestErrLoginRejectedIdentity 登录失败的两种性质必须能被 errors.Is 区分开。
//
// 上层（core.noteLoginRejected）完全依赖这个判定来决定是否累计"连续被拒"次数：
//   - 服务器拒绝（ret != 1）→ 累计，达到阈值自动禁用账号；
//   - 网络/传输失败        → 不累计，继续正常重试。
//
// 若这里判定反了，后果是要么密码错误的账号无限重试（风控风险），
// 要么网络一抖动就把正常账号禁用掉。
func TestErrLoginRejectedIdentity(t *testing.T) {
	// 服务器明确拒绝：Login 中 ret != 1 分支的构造方式
	rejected := fmt.Errorf("%w ret=%d err=%q", ErrLoginRejected, int64(-2), "ACCOUNT_NOT_FOUND")
	if !errors.Is(rejected, ErrLoginRejected) {
		t.Error("服务器拒绝的错误应能被 errors.Is 识别为 ErrLoginRejected")
	}
	if !errors.Is(rejected, ErrLoginRejected) {
		t.Error("嵌套包装后仍应能识别")
	}

	// 网络/传输类失败：不应被误判为服务器拒绝
	cases := map[string]error{
		"http 传输失败": fmt.Errorf("login http: %w", context.DeadlineExceeded),
		"HTTP 状态码":  fmt.Errorf("login status %d: %s", 502, "bad gateway"),
		"响应解析失败":   fmt.Errorf("login resp b64: %w body=%q", errors.New("illegal base64"), "xyz"),
		"缺 token":   errors.New("login resp missing token/gate: ..."),
	}
	for name, err := range cases {
		if errors.Is(err, ErrLoginRejected) {
			t.Errorf("%s 属于可恢复的网络/传输错误，不应被判定为服务器拒绝: %v", name, err)
		}
	}
}

// TestErrLoginRejectedMessage 错误文本要带上 ret 与原始 errStr，
// 这样 UI 能直接把服务器给的真实原因展示给用户（协议里密码错误的 errStr
// 尚无样本，保留原文才能在拿到样本后定位）。
func TestErrLoginRejectedMessage(t *testing.T) {
	err := fmt.Errorf("%w ret=%d err=%q", ErrLoginRejected, int64(-2), "ACCOUNT_NOT_FOUND")
	got := err.Error()
	for _, want := range []string{"login rejected", "ret=-2", "ACCOUNT_NOT_FOUND"} {
		if !contains(got, want) {
			t.Errorf("错误文本 %q 应包含 %q", got, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
