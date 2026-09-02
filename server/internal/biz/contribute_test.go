package biz

import "testing"

// TestRoundContributeAmount 捐献量必须规整到 100 的倍数，否则服务器整单拒绝
func TestRoundContributeAmount(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"非整百向下取整", 3250, 3200},
		{"余额裁剪出的零头", 3137, 3100},
		{"本身是整百则不变", 3000, 3000},
		{"小于一个步长归零", 99, 0},
		{"正好一个步长", 100, 100},
		{"零", 0, 0},
		{"负数归零", -50, 0},
	}
	for _, c := range cases {
		if got := roundContributeAmount(c.in); got != c.want {
			t.Errorf("%s: roundContributeAmount(%d) = %d, want %d",
				c.name, c.in, got, c.want)
		}
	}
}

// TestRoundContributeAmountNeverExceeds 规整后的量不得超过原量（不能把余额捐到下限以下）
func TestRoundContributeAmountNeverExceeds(t *testing.T) {
	for _, in := range []int{1, 99, 100, 101, 999, 3250, 12345} {
		got := roundContributeAmount(in)
		if got > in {
			t.Errorf("roundContributeAmount(%d) = %d 超过了原值，会导致余额被捐到下限以下", in, got)
		}
		if got < 0 {
			t.Errorf("roundContributeAmount(%d) = %d 不应为负", in, got)
		}
		if in > 0 && got%contributeAmountStep != 0 {
			t.Errorf("roundContributeAmount(%d) = %d 不是 %d 的倍数", in, got, contributeAmountStep)
		}
	}
}
