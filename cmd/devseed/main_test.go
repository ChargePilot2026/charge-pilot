package main

import "testing"

// TestEndedOrderMustCarryEndedAt 钉住一条示例数据的硬要求：已结束的订单必须
// 带 ended_at。
//
// 这不是洁癖。首页看板的"近 7 天完成订单与结算金额"是按 ended_at 分天汇总的，
// 结算筛选也认它。示例数据一律留空的话，运营打开后台第一眼看到的是一张全 0
// 的趋势表，订单列表的"结束时间"列也全是"—"——看起来像平台没数据，实际上是
// 示例数据自己缺字段。
//
// 这里测的是纯函数式的口径判定：已结束的两种状态要时间，仍在充电的不要。
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
