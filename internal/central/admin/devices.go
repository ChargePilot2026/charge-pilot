package admin

import (
	"context"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Device struct {
	ID          uint64     `json:"id"`
	DeviceID    string     `json:"device_id"`
	StationID   *uint64    `json:"station_id"`
	StationName *string    `json:"station_name"`
	StationCode *string    `json:"station_code"`
	VendorID    *uint64    `json:"vendor_id"`
	Model       *string    `json:"model"`
	Status      string     `json:"status"`
	InstallAt   *time.Time `json:"install_at"`
}

func (s ResourceStore) deviceQuery(ctx context.Context) *gorm.DB {
	return s.AdminDB.WithContext(ctx).Table("device_meta AS d").Joins("LEFT JOIN station AS s ON s.id=d.station_id AND s.deleted_at IS NULL").Where("d.deleted_at IS NULL")
}

const deviceColumns = "d.id,d.device_id,d.station_id,d.vendor_id,d.model,d.status,d.install_at,s.name AS station_name,s.code AS station_code"

func (s ResourceStore) Devices(ctx context.Context, q PageQuery) (Page[Device], error) {
	out := Page[Device]{Items: []Device{}, Page: q.Page, PageSize: q.PageSize}
	query := s.deviceQuery(ctx)
	if q.Status != "" {
		query = query.Where("d.status = ?", q.Status)
	}
	if q.Keyword != "" {
		v := likePattern(q.Keyword)
		query = query.Where("(d.device_id LIKE ? ESCAPE '!' OR d.model LIKE ? ESCAPE '!' OR s.name LIKE ? ESCAPE '!' OR s.code LIKE ? ESCAPE '!')", v, v, v, v)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Select(deviceColumns).Order("d.id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Scan(&out.Items).Error
	return out, err
}
func (a ResourceAPI) devices(c *gin.Context) {
	q, ok := parsePage(c, "enabled disabled retired fault")
	if !ok {
		return
	}
	out, err := a.Store.Devices(c.Request.Context(), q)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	out.Permissions = c.MustGet("admin_profile").(Profile).Permissions
	httpapi.OK(c, out)
}
func (a ResourceAPI) device(c *gin.Context) {
	id := c.Param("id")
	if len(id) > 64 {
		httpapi.BadRequest(c, "设备编号过长")
		return
	}
	var row Device
	if err := a.Store.deviceQuery(c.Request.Context()).Select(deviceColumns).Where("d.device_id = ?", id).Take(&row).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}
