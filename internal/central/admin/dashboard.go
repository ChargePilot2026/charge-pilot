package admin

import (
	"context"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Dashboard 是管理端首页看板的只读数据源,只持有连接、不保存任何状态。
// UserDB 指向用户库,订单和金额类指标都从 charge_order 统计;
// AdminDB 指向管理库,告警条数从 alert_event 统计。
type Dashboard struct{ UserDB, AdminDB *gorm.DB }

// TrendDay 是首页趋势图的一格,按东八区自然日聚合已结束的充电订单。
type TrendDay struct {
	Day             string `json:"day"`              // 统计日期,格式 YYYY-MM-DD,已按东八区换算
	CompletedOrders int64  `json:"completed_orders"` // 当天已结束(ended_at 非空且已计费)的充电订单数
	SettledCents    int64  `json:"settled_cents"`    // 当天已结束订单的合计金额,单位分
}

// Metrics 是首页看板一次读取的全部指标:充电中订单、今日下单用户、今日已结束订单与金额、
// 未处理告警,以及最近 7 天趋势。任一子查询失败就整体返回错误,前端显示“数据暂不可用”而不是半份数据。
type Metrics struct {
	ChargingOrders       int64      `json:"charging_orders"`        // 当前状态为 charging(充电中)的订单数
	TodayOrderUsers      int64      `json:"today_order_users"`      // 今日(东八区)创建过订单的去重用户数
	TodayCompletedOrders int64      `json:"today_completed_orders"` // 今日已结束订单数,取自 DailyTrend 的最后一格
	TodaySettledCents    int64      `json:"today_settled_cents"`    // 今日已结束订单的合计金额,单位分
	ActiveAlerts         int64      `json:"active_alerts"`          // 未处理告警数,统计 alert_event 里 active 与 acknowledged 两种状态
	DailyTrend           []TrendDay `json:"daily_trend"`            // 最近 7 天(含今天)的趋势,按天升序固定 7 格,没有数据的天补 0
	UpdatedAt            time.Time  `json:"updated_at"`             // 本次快照的生成时间(UTC)
}

// Read 汇总首页看板指标:先按东八区切出“今天”,再回查最近 7 天(含今天)的趋势,
// 并把今天的格子顺带填进今日指标,避免为同一个数字再查一次库。
func (d Dashboard) Read(ctx context.Context, now time.Time) (Metrics, error) {
	local := now.In(time.FixedZone("Asia/Shanghai", 8*3600))
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	m := Metrics{UpdatedAt: now.UTC(), DailyTrend: make([]TrendDay, 0, 7)}
	orders := d.UserDB.WithContext(ctx).Table("charge_order").Where("deleted_at IS NULL")
	if err := orders.Session(&gorm.Session{}).Where("status = 'charging'").Count(&m.ChargingOrders).Error; err != nil {
		return m, err
	}
	if err := orders.Session(&gorm.Session{}).Where("created_at >= ? AND created_at < ?", today.UTC(), today.AddDate(0, 0, 1).UTC()).Distinct("user_id").Count(&m.TodayOrderUsers).Error; err != nil {
		return m, err
	}
	var rows []TrendDay
	err := orders.Session(&gorm.Session{}).Select("DATE_FORMAT(DATE_ADD(ended_at, INTERVAL 8 HOUR), '%Y-%m-%d') AS day, COUNT(*) AS completed_orders, COALESCE(SUM(total_cents),0) AS settled_cents").Where("ended_at >= ? AND ended_at < ? AND total_cents IS NOT NULL", today.AddDate(0, 0, -6).UTC(), today.AddDate(0, 0, 1).UTC()).Group("day").Find(&rows).Error
	if err != nil {
		return m, err
	}
	byDay := map[string]TrendDay{}
	for _, row := range rows {
		byDay[row.Day] = row
	}
	for i := -6; i <= 0; i++ {
		day := today.AddDate(0, 0, i).Format("2006-01-02")
		row := byDay[day]
		row.Day = day
		m.DailyTrend = append(m.DailyTrend, row)
		if i == 0 {
			m.TodayCompletedOrders = row.CompletedOrders
			m.TodaySettledCents = row.SettledCents
		}
	}
	err = d.AdminDB.WithContext(ctx).Table("alert_event").Where("status IN ('active','acknowledged')").Count(&m.ActiveAlerts).Error
	return m, err
}

// Register 注册 GET /api/v1/admin/dashboard,需要 dashboard.read 权限;
// 读取失败时返回 503 而不是空指标,避免看板被误读成“业务正常”。
func (d Dashboard) Register(r *gin.Engine, a API) {
	r.GET("/api/v1/admin/dashboard", a.Require("dashboard.read"), func(c *gin.Context) {
		m, err := d.Read(c.Request.Context(), time.Now())
		if err != nil {
			httpapi.Write(c, 503, 5003, "仪表盘数据暂不可用", nil)
			return
		}
		httpapi.OK(c, m)
	})
}
