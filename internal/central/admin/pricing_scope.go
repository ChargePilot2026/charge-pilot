package admin

import (
	"errors"
	"gorm.io/gorm"
	"regexp"
)

var errAlreadyReported = errors.New("response already written")
var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type pricingTemplateRow struct {
	ID          uint64 `gorm:"column:id"`           // 模板主键
	Name        string `gorm:"column:name"`         // 模板名称
	Remark      string `gorm:"column:remark"`       // 模板备注
	SpecJSON    []byte `gorm:"column:spec_json"`    // 计费口径原文，对应 pricing.Spec；可能存的是过期口径，发布前要重新校验
	DisplayJSON []byte `gorm:"column:display_json"` // 用户端展示开关原文，对应 pricing.Display
	Status      string `gorm:"column:status"`       // 模板状态：active 可被应用；disabled 只允许查看，不允许再下发
	Version     uint32 `gorm:"column:version"`      // 模板版本号，每次修改或停用都 +1，用作 expected_version 乐观锁
}

func ruleScope(stationID uint64, deviceID string) func(*gorm.DB) *gorm.DB {
	return func(q *gorm.DB) *gorm.DB {
		if deviceID == "" {
			return q.Where("station_id=? AND device_id IS NULL", stationID)
		}
		return q.Where("station_id=? AND device_id=?", stationID, deviceID)
	}
}

func nullableDevice(deviceID string) any {
	if deviceID == "" {
		return nil
	}
	return deviceID
}
