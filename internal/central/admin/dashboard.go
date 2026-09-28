package admin

import (
	"context"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Dashboard struct{ UserDB, AdminDB *gorm.DB }
type TrendDay struct {
	Day             string `json:"day"`
	CompletedOrders int64  `json:"completed_orders"`
	SettledCents    int64  `json:"settled_cents"`
}
type Metrics struct {
	ChargingOrders       int64      `json:"charging_orders"`
	TodayOrderUsers      int64      `json:"today_order_users"`
	TodayCompletedOrders int64      `json:"today_completed_orders"`
	TodaySettledCents    int64      `json:"today_settled_cents"`
	ActiveAlerts         int64      `json:"active_alerts"`
	DailyTrend           []TrendDay `json:"daily_trend"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

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
