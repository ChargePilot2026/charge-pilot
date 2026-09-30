package control

import (
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"time"
)

type DeviceSummaryAPI struct {
	DB           *gorm.DB
	ServiceToken string
}

func (a DeviceSummaryAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/internal/device-summaries", a.list)
}

func (a DeviceSummaryAPI) list(c *gin.Context) {
	if !(SessionAPI{ServiceToken: a.ServiceToken}).authorized(c) {
		return
	}
	ids := c.QueryArray("device_id")
	if len(ids) == 0 || len(ids) > 100 {
		httpapi.BadRequest(c, "请选择1至100台设备")
		return
	}
	for _, id := range ids {
		if len(id) == 0 || len(id) > 64 {
			httpapi.BadRequest(c, "设备编号无效")
			return
		}
	}
	rows := []struct {
		DeviceID        string     `json:"device_id"`
		VendorName      *string    `json:"vendor_name"`
		LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	}{}
	if err := a.DB.WithContext(c.Request.Context()).Table("device d").Select("d.device_id,v.vendor_name,d.last_heartbeat_at").Joins("LEFT JOIN vendor v ON v.id=d.vendor_id AND v.deleted_at IS NULL").Where("d.device_id IN ? AND d.deleted_at IS NULL", ids).Scan(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5001, "设备信息暂不可读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}
