package admin

import (
	"context"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Dashboard 是管理端首页看板的只读数据源，只持有连接、不保存任何状态。
// UserDB 指向用户库，订单、金额与用户指标从 charge_order 和 user 统计；
// AdminDB 指向管理库，站点、设备与告警数量分别从所属表统计。
type Dashboard struct{ UserDB, AdminDB *gorm.DB }

// TrendDay 是首页趋势图的一格，按东八区自然日聚合已结束的充电订单。
type TrendDay struct {
	Day             string `json:"day"`              // 统计日期,格式 YYYY-MM-DD,已按东八区换算
	CompletedOrders int64  `json:"completed_orders"` // 当天已结束(ended_at 非空且已计费)的充电订单数
	SettledCents    int64  `json:"settled_cents"`    // 当天已结束订单的合计金额,单位分
}

// Metrics 是首页看板一次读取的订单、用户、站点、设备和告警指标，以及最近 7 天趋势。
// 任一子查询失败就整体返回错误，前端显示“数据暂不可用”而不是半份数据。
type Metrics struct {
	ChargingOrders       int64      `json:"charging_orders"`        // 当前状态为 charging(充电中)的订单数
	TodayOrders          int64      `json:"today_orders"`           // 今日(北京时间)创建的未删除订单数，包含未启动订单
	TotalUsers           int64      `json:"total_users"`            // 未删除的充电用户总数
	NewUsers             int64      `json:"new_users"`              // 今日(北京时间)注册的未删除充电用户数
	TodayChargingUsers   int64      `json:"today_charging_users"`   // 今日(北京时间)实际开始过充电的去重用户数，按 started_at 统计
	StationCount         int64      `json:"station_count"`          // 未删除的管理库站点数
	DeviceCount          int64      `json:"device_count"`           // 未删除的管理库设备元数据数
	TodayOrderUsers      int64      `json:"today_order_users"`      // 今日(东八区)创建过订单的去重用户数
	TodayCompletedOrders int64      `json:"today_completed_orders"` // 今日已结束订单数,取自 DailyTrend 的最后一格
	TodaySettledCents    int64      `json:"today_settled_cents"`    // 今日已结束订单的合计金额,单位分
	ActiveAlerts         int64      `json:"active_alerts"`          // 未处理告警数,统计 alert_event 里 active 与 acknowledged 两种状态
	DailyTrend           []TrendDay `json:"daily_trend"`            // 最近 7 天(含今天)的趋势,按天升序固定 7 格,没有数据的天补 0
	UpdatedAt            time.Time  `json:"updated_at"`             // 本次快照的生成时间(UTC)
}

// Read 汇总首页看板指标：先按东八区切出“今天”，再回查最近 7 天（含今天）的趋势，
// 并把今天的格子顺带填进今日指标，避免为同一个数字再查一次库。
func (d Dashboard) Read(ctx context.Context, now time.Time) (Metrics, error) {
	local := now.In(time.FixedZone("Asia/Shanghai", 8*3600))
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	start, end := today.UTC(), today.AddDate(0, 0, 1).UTC()
	m := Metrics{UpdatedAt: now.UTC(), DailyTrend: make([]TrendDay, 0, 7)}
	orders := d.UserDB.WithContext(ctx).Table("charge_order").Where("deleted_at IS NULL")
	if err := orders.Session(&gorm.Session{}).Where("status = 'charging'").Count(&m.ChargingOrders).Error; err != nil {
		return m, err
	}
	if err := orders.Session(&gorm.Session{}).Where("created_at >= ? AND created_at < ?", start, end).Count(&m.TodayOrders).Error; err != nil {
		return m, err
	}
	if err := orders.Session(&gorm.Session{}).Where("created_at >= ? AND created_at < ?", start, end).Distinct("user_id").Count(&m.TodayOrderUsers).Error; err != nil {
		return m, err
	}
	if err := orders.Session(&gorm.Session{}).Where("started_at >= ? AND started_at < ?", start, end).Distinct("user_id").Count(&m.TodayChargingUsers).Error; err != nil {
		return m, err
	}
	users := d.UserDB.WithContext(ctx).Table("user").Where("deleted_at IS NULL")
	if err := users.Session(&gorm.Session{}).Count(&m.TotalUsers).Error; err != nil {
		return m, err
	}
	if err := users.Session(&gorm.Session{}).Where("created_at >= ? AND created_at < ?", start, end).Count(&m.NewUsers).Error; err != nil {
		return m, err
	}
	if err := d.AdminDB.WithContext(ctx).Table("station").Where("deleted_at IS NULL").Count(&m.StationCount).Error; err != nil {
		return m, err
	}
	if err := d.AdminDB.WithContext(ctx).Table("device_meta").Where("deleted_at IS NULL").Count(&m.DeviceCount).Error; err != nil {
		return m, err
	}
	var rows []TrendDay
	err := orders.Session(&gorm.Session{}).Select("DATE_FORMAT(DATE_ADD(ended_at, INTERVAL 8 HOUR), '%Y-%m-%d') AS day, COUNT(*) AS completed_orders, COALESCE(SUM(total_cents),0) AS settled_cents").Where("ended_at >= ? AND ended_at < ? AND total_cents IS NOT NULL", today.AddDate(0, 0, -6).UTC(), end).Group("day").Find(&rows).Error
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

// Register 注册 GET /api/v1/admin/dashboard，需要 dashboard.read 权限；
// 读取失败时返回 503 而不是空指标，避免看板被误读成“业务正常”。
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
