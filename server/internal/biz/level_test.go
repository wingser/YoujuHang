package biz

import "testing"

// TestLevelProgress 升级进度换算（2026-09-02 抓包 + 客户端逆向实证）
//
// 背景：514 的 id=10001 / id=10002 是游戏内置等级经验表的**相邻两项**
// （当前等级起始 / 下一等级起始），是静态值；真正的进度必须由总经验推算。
//
// 期望值均来自真实账号实测，其中"赛博大善人"一组与游戏客户端截图
// 逐位一致（客户端显示 165/220）。
func TestLevelProgress(t *testing.T) {
	cases := []struct {
		name       string
		exp        uint64 // 总经验（514: id=10000）
		levelStart uint64 // 514: id=10001
		levelNext  uint64 // 514: id=10002
		wantGot    uint64 // 客户端"已获得"
		wantTotal  uint64 // 客户端"升级所需"
	}{
		// 与游戏客户端截图完全一致：显示 165/220
		{"赛博大善人 Lv9", 120527, 1040, 1260, 165, 220},
		// chouyoku Lv178（对应等级表索引 177 / 178）
		{"chouyoku Lv178", 199131963, 1971612, 1994496, 19707, 22884},
		// wingser Lv184（对应等级表索引 183 / 184）
		{"wingser Lv184", 212778411, 2110896, 2134572, 16888, 23676},
		// 边界：刚好停在当前等级起始 → 进度 0
		{"刚进入本级", 104000, 1040, 1260, 0, 220},
		// 边界：总经验低于等级起始（异常数据不应下溢）
		{"经验倒挂", 100, 1040, 1260, 0, 220},
		// 边界：下一级起始不大于当前起始（异常表数据）
		{"表数据异常", 120527, 1260, 1040, 0, 0},
	}
	for _, c := range cases {
		s := UserStats{Exp: c.exp, LevelStart: c.levelStart, LevelNext: c.levelNext}
		if got := s.LevelGot(); got != c.wantGot {
			t.Errorf("%s: LevelGot() = %d, 期望 %d", c.name, got, c.wantGot)
		}
		if total := s.LevelTotal(); total != c.wantTotal {
			t.Errorf("%s: LevelTotal() = %d, 期望 %d", c.name, total, c.wantTotal)
		}
	}
}

// TestLevelProgressAdvances 经验增长时升级进度必须同步上升。
//
// 这是旧 bug 的回归测试：旧实现直接用 10001/10002 当分子分母，
// 而这两个值是静态的等级表项，导致百分比长期恒定（chouyoku 一直显示 98.85%）。
func TestLevelProgressAdvances(t *testing.T) {
	// chouyoku 实测：9/1 总经验 199028916 → 9/2 总经验 199131963（+103047）
	before := UserStats{Exp: 199028916, LevelStart: 1971612, LevelNext: 1994496}
	after := UserStats{Exp: 199131963, LevelStart: 1971612, LevelNext: 1994496}

	if after.LevelGot() <= before.LevelGot() {
		t.Errorf("总经验增长后进度应上升：9/1 got=%d, 9/2 got=%d",
			before.LevelGot(), after.LevelGot())
	}
	// 旧实现在此处两者相等（恒定不变），确保不再回退
	if before.LevelGot() == after.LevelGot() {
		t.Errorf("进度不应恒定不变（旧 bug：误用静态的 10001/10002 作分子分母）")
	}
}
