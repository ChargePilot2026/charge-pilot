package admin

import (
	"context"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Station struct {
	ID           uint64  `json:"id" gorm:"primaryKey"`
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Address      *string `json:"address"`
	Longitude    float64 `json:"longitude"`
	Latitude     float64 `json:"latitude"`
	Status       string  `json:"status"`
	OpenHours    *string `json:"open_hours"`
	ContactPhone *string `json:"contact_phone"`
}

func (Station) TableName() string { return "station" }

type StationInput struct {
	Code         *string  `json:"code"`
	Name         string   `json:"name"`
	Address      *string  `json:"address"`
	Longitude    *float64 `json:"longitude"`
	Latitude     *float64 `json:"latitude"`
	Status       string   `json:"status"`
	OpenHours    *string  `json:"open_hours"`
	ContactPhone *string  `json:"contact_phone"`
}

var stationCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (input StationInput) valid(create bool) bool {
	if strings.TrimSpace(input.Name) == "" || utf8.RuneCountInString(input.Name) > 128 || input.Longitude == nil || input.Latitude == nil || math.IsNaN(*input.Longitude) || math.IsNaN(*input.Latitude) || math.Abs(*input.Longitude) > 180 || math.Abs(*input.Latitude) > 90 || input.Status == "" || !oneOf(input.Status, "active disabled construction") {
		return false
	}
	if create && (input.Code == nil || !stationCodePattern.MatchString(*input.Code)) {
		return false
	}
	for value, max := range map[*string]int{input.Address: 255, input.OpenHours: 64, input.ContactPhone: 32} {
		if value != nil && utf8.RuneCountInString(*value) > max {
			return false
		}
	}
	return true
}
func (s ResourceStore) Stations(ctx context.Context, q PageQuery) (Page[Station], error) {
	out := Page[Station]{Items: []Station{}, Page: q.Page, PageSize: q.PageSize}
	query := s.AdminDB.WithContext(ctx).Model(&Station{}).Where("deleted_at IS NULL")
	if q.Status != "" {
		query = query.Where("status = ?", q.Status)
	}
	if q.Keyword != "" {
		pattern := likePattern(q.Keyword)
		query = query.Where("(code LIKE ? ESCAPE '!' OR name LIKE ? ESCAPE '!' OR address LIKE ? ESCAPE '!')", pattern, pattern, pattern)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := query.Order("id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error
	return out, err
}
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
func (a ResourceAPI) createStation(c *gin.Context) { a.saveStation(c, true) }
func (a ResourceAPI) updateStation(c *gin.Context) { a.saveStation(c, false) }
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
	if !input.valid(create) || (!create && input.Code != nil) {
		httpapi.BadRequest(c, "站点参数无效：请检查名称、编码、经纬度和状态；编码不可修改")
		return
	}
	row := Station{ID: id, Name: strings.TrimSpace(input.Name), Address: input.Address, Longitude: *input.Longitude, Latitude: *input.Latitude, Status: input.Status, OpenHours: input.OpenHours, ContactPhone: input.ContactPhone}
	if create {
		row.Code = *input.Code
	}
	p := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if create {
			// The stable registry serializes concurrent creates and prevents code reuse.
			if err := tx.Table("station_code_identity").Create(map[string]any{"code": row.Code}).Error; err != nil {
				return err
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
		row.Code = before.Code
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
