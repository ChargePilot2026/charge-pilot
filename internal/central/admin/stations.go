package admin

import (
	"context"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 站点曾经有一份对外业务编码 code：它与客户资产台账上的编码保持一致，C 端详情
// 接口也按它寻址（GET /api/v1/user/station/:code）。这套编码还有一条更强的约束——
// 全局唯一且删除后永不复用，仅靠 (code, deleted_at) 唯一索引做不到，因为 MySQL 把
// 唯一索引里的每个 NULL 都视为互不相同，两个存活站点可以同时 code = NULL 而谁也
// 拦不住；真正兜底的是 station_code_identity 注册表。
//
// 迁移 admin_db/0044 同时删掉了 code 列和这张注册表，站点现在统一用主键 ID 寻址。
// StationInput.Code 仍然保留，但它的用途已经变成"拦截旧客户端"：仍在提交 code 的
// 客户端会拿到一句明确的提示，而不是让这个值在解码时被静默丢弃。

// Station 是站点的对外模型，映射 admin_db 的 station 表。站点是整套计费配置的
// 挂载点——分账模板、计费规则、充电套餐都挂在站点 ID 上。站点已无独立的业务编码
// 字段，列表、详情和所有下级配置的引用一律使用主键 ID。
type Station struct {
	ID              uint64  `json:"id" gorm:"primaryKey"`                              // 站点主键，也是分账模板、计费规则、充电套餐引用站点的外键。
	Name            string  `json:"name"`                                              // 站点名称，列表展示与关键词搜索都匹配它。
	Address         *string `json:"address"`                                           // 站点地址，可空；关键词搜索会同时匹配名称和地址。
	Longitude       float64 `json:"longitude"`                                         // 经度，有效范围 [-180,180]，站点地图定位用。
	Latitude        float64 `json:"latitude"`                                          // 纬度，有效范围 [-90,90]。
	Status          string  `json:"status"`                                            // 站点状态：active 营业中、disabled 停用、construction 建设中。
	OpenHours       *string `json:"open_hours"`                                        // 营业时间自由文本，可空，最长 64 字符。
	ContactPhone    *string `json:"contact_phone"`                                     // 对外联系电话，可空，最长 32 字符。
	SplitTemplateID *uint64 `json:"split_template_id" gorm:"column:split_template_id"` // 已绑定的分账模板 ID，可空表示未绑定；只能在独立的绑定接口里改，不随站点编辑变更。
}

// TableName 指定 Station 落在 admin_db 的 station 表上。
func (Station) TableName() string { return "station" }

// StationInput 是站点新建/编辑的请求体。经度、纬度用指针是为了区分"没传"和
// "传了 0"——这两个字段是必填项，缺失按无效处理。分账模板同样用指针表示"不绑定"，
// 但它只在新建时生效，编辑路径一旦带上就会被拒绝。
type StationInput struct {
	// Code is not a field of the station any more. It is kept here only so a
	// client still sending it gets told why, instead of having the value
	// silently dropped on decode.
	Code            *string  `json:"code"`              // 已废弃的站点编码，故意保留的兼容字段：非 nil 即触发"编码已移除"的报错，不要当成待删的死代码。
	Name            string   `json:"name"`              // 站点名称，必填，去空白后最长 128 字符。
	Address         *string  `json:"address"`           // 站点地址，可空，最长 255 字符。
	Longitude       *float64 `json:"longitude"`         // 经度，必填指针；缺失、非数字或超出 [-180,180] 判为无效。
	Latitude        *float64 `json:"latitude"`          // 纬度，必填指针；缺失、非数字或超出 [-90,90] 判为无效。
	Status          string   `json:"status"`            // 站点状态，必填：active / disabled / construction 三选一。
	OpenHours       *string  `json:"open_hours"`        // 营业时间，可空，最长 64 字符。
	ContactPhone    *string  `json:"contact_phone"`     // 联系电话，可空，最长 32 字符。
	SplitTemplateID *uint64  `json:"split_template_id"` // 分账模板 ID，可空；指向 0 视为无效，新建时还会校验模板可用，编辑时携带该字段直接拒绝。
}

// valid 校验站点写入参数：名称必填且不超过 128 字符，经纬度必填、不能为 NaN 且落在
// 合法区间内，状态必须是 active/disabled/construction 之一，分账模板 ID 不得为 0，
// 三个可空文本字段各有长度上限。任一不满足即返回 false。
func (input StationInput) valid() bool {
	if strings.TrimSpace(input.Name) == "" || utf8.RuneCountInString(input.Name) > 128 || input.Longitude == nil || input.Latitude == nil || math.IsNaN(*input.Longitude) || math.IsNaN(*input.Latitude) || math.Abs(*input.Longitude) > 180 || math.Abs(*input.Latitude) > 90 || input.Status == "" || !oneOf(input.Status, "active disabled construction") {
		return false
	}
	if input.SplitTemplateID != nil && *input.SplitTemplateID == 0 {
		return false
	}
	for value, max := range map[*string]int{input.Address: 255, input.OpenHours: 64, input.ContactPhone: 32} {
		if value != nil && utf8.RuneCountInString(*value) > max {
			return false
		}
	}
	return true
}

// Stations 分页查询未删除的站点，支持按状态过滤和按名称/地址模糊搜索（通配符已转义），
// 先 Count 再按 ID 倒序取当页，最后把分页信息原样带回给调用方组装响应。
func (s ResourceStore) Stations(ctx context.Context, q PageQuery) (Page[Station], error) {
	out := Page[Station]{Items: []Station{}, Page: q.Page, PageSize: q.PageSize}
	query := s.AdminDB.WithContext(ctx).Model(&Station{}).Where("deleted_at IS NULL")
	if q.Status != "" {
		query = query.Where("status = ?", q.Status)
	}
	if q.Keyword != "" {
		pattern := likePattern(q.Keyword)
		query = query.Where("(name LIKE ? ESCAPE '!' OR address LIKE ? ESCAPE '!')", pattern, pattern)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Order("id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error
	return out, err
}

// stations 响应站点分页列表，状态入参只接受 active/disabled/construction，
// 并把当前管理员的权限列表一并带回，供前端决定按钮的可见性。
func (a ResourceAPI) stations(c *gin.Context) {
	q, ok := parsePage(c, "active disabled construction")
	if !ok {
		return
	}
	out, err := a.Store.Stations(c.Request.Context(), q)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	out.Permissions = c.MustGet("admin_profile").(Profile).Permissions
	httpapi.OK(c, out)
}

// station 按主键返回单个站点详情；已软删除的站点等同不存在，由 resourceFailure 回 404。
func (a ResourceAPI) station(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var row Station
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// createStation 是新建站点的 HTTP 入口，转调 saveStation 的新建分支。
func (a ResourceAPI) createStation(c *gin.Context) { a.saveStation(c, true) }

// updateStation 是更新站点的 HTTP 入口，转调 saveStation 的更新分支。
func (a ResourceAPI) updateStation(c *gin.Context) { a.saveStation(c, false) }

// saveStation 新建或更新一个站点。三条硬约束：请求里出现 code 字段直接回 400 并说明
// 编码已移除；更新路径一旦带 split_template_id 就判为无效，分账模板必须走独立的绑定
// 接口；更新时先对站点行加 UPDATE 行锁再写，使审计里的 before 与真正被改掉的内容一致。
// 新建分支还会校验分账模板处于可用状态；两条分支都在同一事务里写审计。
func (a ResourceAPI) saveStation(c *gin.Context, create bool) {
	var id uint64
	if !create {
		var ok bool
		id, ok = pathID(c)
		if !ok {
			return
		}
	}
	var input StationInput
	if !decodeResource(c, &input) {
		return
	}
	if input.Code != nil {
		httpapi.BadRequest(c, "编码已移除：站点统一使用主键 ID，请勿提交 code 字段")
		return
	}
	if !input.valid() || (!create && input.SplitTemplateID != nil) {
		httpapi.BadRequest(c, "站点参数无效：请检查名称、经纬度和状态")
		return
	}
	row := Station{ID: id, Name: strings.TrimSpace(input.Name), Address: input.Address, Longitude: *input.Longitude, Latitude: *input.Latitude, Status: input.Status, OpenHours: input.OpenHours, ContactPhone: input.ContactPhone, SplitTemplateID: input.SplitTemplateID}
	p := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if create {
			if input.SplitTemplateID != nil {
				if err := requireUsableSplitTemplate(tx, *input.SplitTemplateID); err != nil {
					return err
				}
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			return resourceAudit(tx, p, "create", "station", row.ID, nil, row, c.ClientIP(), httpapi.RequestID(c))
		}
		var before Station
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		row.SplitTemplateID = before.SplitTemplateID
		if err := tx.Model(&Station{}).Where("id = ?", id).Updates(map[string]any{"name": row.Name, "address": row.Address, "longitude": row.Longitude, "latitude": row.Latitude, "status": row.Status, "open_hours": row.OpenHours, "contact_phone": row.ContactPhone}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, p, "update", "station", id, before, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}
