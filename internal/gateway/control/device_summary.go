package control

import (
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type DevicePortSummary struct {
	PortNo     uint8      `json:"port_no"`
	StatusCode *uint8     `json:"status_code" gorm:"column:reported_status"`
	StatusAt   *time.Time `json:"status_at" gorm:"column:reported_status_at"`
}

type DeviceSummary struct {
	DeviceID        string              `json:"device_id"`
	VendorName      *string             `json:"vendor_name"`
	LastHeartbeatAt *time.Time          `json:"last_heartbeat_at"`
	SignalStrength  *uint8              `json:"signal_strength"`
	SignalAt        *time.Time          `json:"signal_at"`
	Ports           []DevicePortSummary `json:"ports" gorm:"-"`
}

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
	rows := []DeviceSummary{}
	if err := a.DB.WithContext(c.Request.Context()).Table("device d").Select("d.device_id,v.vendor_name,d.last_heartbeat_at,d.signal_strength,d.signal_at").Joins("LEFT JOIN vendor v ON v.id=d.vendor_id AND v.deleted_at IS NULL").Where("d.device_id IN ? AND d.deleted_at IS NULL", ids).Scan(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5001, "设备信息暂不可读取", nil)
		return
	}
	byDevice := make(map[string]int, len(rows))
	for i := range rows {
		rows[i].Ports = []DevicePortSummary{}
		byDevice[rows[i].DeviceID] = i
	}
	if len(rows) > 0 {
		var ports []struct {
			DeviceID          string
			DevicePortSummary `gorm:"embedded"`
		}
		if err := a.DB.WithContext(c.Request.Context()).Table("device_port").
			Select("device_id,port_no,reported_status,reported_status_at").
			Where("device_id IN ? AND deleted_at IS NULL", ids).Order("device_id,port_no").Scan(&ports).Error; err != nil {
			httpapi.Write(c, 503, 5001, "设备端口状态暂不可读取", nil)
			return
		}
		for _, port := range ports {
			if i, ok := byDevice[port.DeviceID]; ok {
				rows[i].Ports = append(rows[i].Ports, port.DevicePortSummary)
			}
		}
	}
	httpapi.OK(c, gin.H{"items": rows})
}
