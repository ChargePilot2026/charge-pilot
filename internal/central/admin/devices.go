package admin

import (
	"context"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Device 是充电桩（设备）的列表/详情模型，取自 admin_db.device_meta 并左连站点带出
// 站点名。站点统一用主键 ID 关联，列表不再返回站点编码，关键词搜索也不再匹配站点编码。
type Device struct {
	ID          uint64     `json:"id"`           // 设备在 admin_db 的内部主键，仅用于列表排序。
	DeviceID    string     `json:"device_id"`    // 设备编号（device_id），最长 64 字符，是设备对外的业务标识，也是详情接口的寻址键。
	StationID   *uint64    `json:"station_id"`   // 所属站点 ID，可空表示尚未归属站点。
	StationName *string    `json:"station_name"` // 所属站点名称，联表带出；站点被软删除时为空。
	VendorID    *uint64    `json:"vendor_id"`    // 设备厂商 ID，可空。
	Model       *string    `json:"model"`        // 设备型号，可空；关键词搜索会匹配它。
	Status      string     `json:"status"`       // 设备状态：enabled 启用、disabled 停用、retired 退役、fault 故障。
	InstallAt   *time.Time `json:"install_at"`   // 安装时间，可空表示尚未记录。
	// What this board can report, and what it is currently charged on. Carried
	// on the device row itself so an operator does not have to open the pricing
	// screen to find out why a tariff will not apply here.
	ChargeMode            string `json:"charge_mode"`             // 该设备当前实际生效的计费方式，取自 pricing 引擎的 ChargeMode（server_realtime_power / server_max_power / server_energy / device_duration / device_energy / device_power）。
	ReportsEnergy         bool   `json:"reports_energy"`          // 协议帧里是否带电量：false 表示这块板报不了电量，只有时长口径能落到它身上。
	ReportsSegmentedPower bool   `json:"reports_segmented_power"` // 协议帧里是否带分段功率：功率档位口径需要它，为 false 时该设备无法应用。
}

// deviceQuery 组装设备的基础查询：device_meta 左连未删除的站点，并排除已软删除的设备。
// 设备的所有查询都从这里派生，保证站点名口径和过滤条件一致。
func (s ResourceStore) deviceQuery(ctx context.Context) *gorm.DB {
	return s.AdminDB.WithContext(ctx).Table("device_meta AS d").Joins("LEFT JOIN station AS s ON s.id=d.station_id AND s.deleted_at IS NULL").Where("d.deleted_at IS NULL")
}

// deviceColumns 是设备列表与详情共用的列清单。刻意不含站点编码——迁移 admin_db/0044
// 之后 station 表已经没有 code 列了。
const deviceColumns = "d.id,d.device_id,d.station_id,d.vendor_id,d.model,d.status,d.install_at,d.charge_mode,d.reports_energy,d.reports_segmented_power,s.name AS station_name"

// Devices 分页查询设备，支持按状态过滤和按设备编号/型号/站点名模糊搜索（通配符已转义）。
// 先 Count 再按内部主键倒序取当页。注意关键词不再匹配站点编码。
func (s ResourceStore) Devices(ctx context.Context, q PageQuery) (Page[Device], error) {
	out := Page[Device]{Items: []Device{}, Page: q.Page, PageSize: q.PageSize}
	query := s.deviceQuery(ctx)
	if q.Status != "" {
		query = query.Where("d.status = ?", q.Status)
	}
	if q.Keyword != "" {
		v := likePattern(q.Keyword)
		query = query.Where("(d.device_id LIKE ? ESCAPE '!' OR d.model LIKE ? ESCAPE '!' OR s.name LIKE ? ESCAPE '!')", v, v, v)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Select(deviceColumns).Order("d.id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Scan(&out.Items).Error
	return out, err
}

// devices 响应设备分页列表，状态入参只接受 enabled/disabled/retired/fault，
// 并把当前管理员的权限列表一并带回，供前端决定按钮的可见性。
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

// device 按设备编号（不是内部主键）返回单台设备详情，编号最长 64 字符。查不到由
// resourceFailure 回 404。
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
