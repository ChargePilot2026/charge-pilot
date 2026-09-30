package admin

import (
	"context"
	"net/url"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Device 是充电桩（设备）的列表/详情模型，取自 admin_db.device_meta 并左连站点带出
// 站点名。站点统一用主键 ID 关联，列表不再返回站点编码，关键词搜索也不再匹配站点编码。
type Device struct {
	ID               uint64     `json:"id"`           // 设备在 admin_db 的内部主键，仅用于列表排序。
	DeviceID         string     `json:"device_id"`    // 设备编号（device_id），最长 64 字符，是设备对外的业务标识，也是详情接口的寻址键。
	StationID        *uint64    `json:"station_id"`   // 所属站点 ID，可空表示尚未归属站点。
	StationName      *string    `json:"station_name"` // 所属站点名称，联表带出；站点被软删除时为空。
	VendorID         *uint64    `json:"vendor_id"`    // 设备厂商 ID，可空。
	VendorName       *string    `json:"vendor_name" gorm:"-"`
	LastHeartbeatAt  *time.Time `json:"last_heartbeat_at" gorm:"-"`
	RuntimeAvailable bool       `json:"runtime_available" gorm:"-"`
	Model            *string    `json:"model"`      // 设备型号，可空；关键词搜索会匹配它。
	Status           string     `json:"status"`     // 设备状态：enabled 启用、disabled 停用、retired 退役、fault 故障。
	InstallAt        *time.Time `json:"install_at"` // 安装时间，可空表示尚未记录。
	// 这块板能上报什么，以及它当前按什么口径计费。
	// 这些信息就挂在设备行上，运营不必再打开定价页面
	// 去弄清某个计费规则为什么在这儿用不了。
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
func (s ResourceStore) Devices(ctx context.Context, q PageQuery, scopes ...DataScope) (Page[Device], error) {
	out := Page[Device]{Items: []Device{}, Page: q.Page, PageSize: q.PageSize}
	query := s.deviceQuery(ctx)
	if q.StationID > 0 {
		query = query.Where("d.station_id=?", q.StationID)
	}
	if len(scopes) > 0 {
		query = scopes[0].ApplyStations(query, "d.station_id")
		query = scopes[0].ApplyVendors(query, "d.vendor_id")
	}
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
	q.StationID, ok = queryStationID(c, false)
	if !ok {
		return
	}
	scope, ok := a.stationScope(c)
	if !ok {
		return
	}
	if q.StationID > 0 && !scope.AllowsStation(q.StationID) {
		httpapi.Write(c, 403, 1003, "该站点不在您的数据范围内", nil)
		return
	}
	out, err := a.Store.Devices(c.Request.Context(), q, scope)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	out.Permissions = c.MustGet("admin_profile").(Profile).Permissions
	a.enrichDevices(c.Request.Context(), out.Items)
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
	scope, ok := a.stationScope(c)
	if !ok {
		return
	}
	query := scope.ApplyVendors(scope.ApplyStations(a.Store.deviceQuery(c.Request.Context()), "d.station_id"), "d.vendor_id")
	if err := query.Select(deviceColumns).Where("d.device_id = ?", id).Take(&row).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	rows := []Device{row}
	a.enrichDevices(c.Request.Context(), rows)
	httpapi.OK(c, rows[0])
}

// Enrich only devices already authorized by the central data scope, in one call.
// A gateway outage must not fabricate a 'never online' result or hide operations.
func (a ResourceAPI) enrichDevices(ctx context.Context, rows []Device) {
	if len(rows) == 0 || a.GatewayURL == "" || a.ServiceToken == "" {
		return
	}
	query := url.Values{}
	for _, row := range rows {
		query.Add("device_id", row.DeviceID)
	}
	var result struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				DeviceID        string     `json:"device_id"`
				VendorName      *string    `json:"vendor_name"`
				LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := (serviceclient.Client{Timeout: 5 * time.Second}).GetJSON(ctx, a.GatewayURL, a.ServiceToken, "/api/v1/internal/device-summaries?"+query.Encode(), &result); err != nil || result.Code != 0 {
		return
	}
	for i := range rows {
		for _, extra := range result.Data.Items {
			if extra.DeviceID == rows[i].DeviceID {
				rows[i].VendorName = extra.VendorName
				rows[i].LastHeartbeatAt = extra.LastHeartbeatAt
				rows[i].RuntimeAvailable = true
				break
			}
		}
	}
}

// Operational suspension leaves the gateway connection and existing orders intact.
func (a ResourceAPI) setDeviceStatus(c *gin.Context) {
	id := c.Param("id")
	var in struct {
		Status string `json:"status"`
	}
	if id == "" || len(id) > 64 {
		httpapi.BadRequest(c, "设备编号无效")
		return
	}
	if !decodeResource(c, &in) {
		return
	}
	if in.Status != "enabled" && in.Status != "disabled" {
		httpapi.BadRequest(c, "运营状态只允许启用或禁用")
		return
	}
	scope, ok := a.stationScope(c)
	if !ok {
		return
	}
	var row Device
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		store := ResourceStore{AdminDB: tx}
		query := scope.ApplyVendors(scope.ApplyStations(store.deviceQuery(c.Request.Context()), "d.station_id"), "d.vendor_id")
		if err := query.Clauses(clause.Locking{Strength: "UPDATE"}).Select(deviceColumns).Where("d.device_id=?", id).Take(&row).Error; err != nil {
			return err
		}
		if row.Status != "enabled" && row.Status != "disabled" {
			return errConflict
		}
		before := row
		row.Status = in.Status
		if err := tx.Table("device_meta").Where("id=?", row.ID).Update("status", in.Status).Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "device.status", "device", row.ID, before, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}
