package main

import "testing"

// TestEndedOrderMustCarryEndedAt 验证示例订单的结束时间规则。
// 已结束状态必须设置 ended_at，充电中状态保持 NULL，以支持看板和订单查询。
func TestEndedOrderMustCarryEndedAt(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{"completed", true},
		{"refunded", true},
		{"charging", false},
		{"cancelled", false},
	}
	for _, c := range cases {
		got := orderHasEndedAt(c.status)
		if got != c.want {
			t.Errorf("状态 %q 的 ended_at 期望有=%v，实际=%v", c.status, c.want, got)
		}
	}
}
