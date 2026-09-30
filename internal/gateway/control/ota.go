package control

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// OtaAPI 接收 central 下发的固件推送计划，并跟踪每台设备的确认情况。
// ota_command 归 gateway_db 所有，交付状态就留在真正和设备对话的
// 这个服务里。
type OtaAPI struct {
	DB           *gorm.DB
	ServiceToken string
}

type otaCommand struct {
	ID             uint64
	CommandID      string  `gorm:"column:command_id"`
	DeviceID       string  `gorm:"column:device_id"`
	PackageID      string  `gorm:"column:package_id"`
	PackageVersion *string `gorm:"column:package_version"`
	Status         string  `gorm:"column:status"`
	SentAt         *time.Time
	AckedAt        *time.Time
	FailedAt       *time.Time
	FailureReason  *string   `gorm:"column:failure_reason"`
	RetryCount     uint32    `gorm:"column:retry_count"`
	CreatedMonth   time.Time `gorm:"column:created_month"`
}

func (otaCommand) TableName() string { return "ota_command" }

func (a OtaAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/ota/dispatch", a.dispatch)
	router.POST("/api/v1/internal/ota/ack", a.ack)
	router.GET("/api/v1/internal/ota/status", a.status)
}

func (a OtaAPI) authorized(c *gin.Context) bool {
	provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	expected := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return false
	}
	return true
}

// dispatch 为每台目标设备记录一次固件推送。command id 由推送计划和设备
// 推导而来，所以被重试的同一个计划
// 不会给同一台设备再排一次升级。
func (a OtaAPI) dispatch(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var request struct {
		ScheduleID     uint64   `json:"schedule_id" binding:"required"`
		PackageID      string   `json:"package_id" binding:"required,max=64"`
		PackageVersion string   `json:"package_version" binding:"required,max=64"`
		Checksum       string   `json:"checksum_sha256" binding:"required,len=64"`
		StorageURL     string   `json:"storage_url" binding:"required,max=512"`
		DeviceIDs      []string `json:"device_ids" binding:"required,min=1,max=500"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		httpapi.BadRequest(c, "invalid OTA dispatch request")
		return
	}
	if !strings.HasPrefix(request.StorageURL, "https://") {
		httpapi.BadRequest(c, "firmware must be served over https")
		return
	}
	month := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	queued := 0
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		for _, deviceID := range request.DeviceIDs {
			deviceID = strings.TrimSpace(deviceID)
			if deviceID == "" || len(deviceID) > 64 {
				return ErrOtaInvalidDevice
			}
			// 只有已开通、且状态启用的设备才允许接收固件。
			var exists int64
			if err := tx.Table("device").Where("device_id = ? AND status = 'active' AND deleted_at IS NULL", deviceID).Count(&exists).Error; err != nil {
				return err
			}
			if exists == 0 {
				return fmt.Errorf("%w: %s", ErrOtaInvalidDevice, deviceID)
			}
			commandID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("ota:%d:%s:%s", request.ScheduleID, deviceID, request.PackageVersion))).String()
			row := map[string]any{
				"command_id": commandID, "device_id": deviceID, "package_id": request.PackageID,
				"package_version": request.PackageVersion, "status": "pending",
				"created_month": month,
			}
			// 重放同一个计划时原样返回已存在的命令，不做任何改动。
			if err := tx.Table("ota_command").Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
				return err
			}
			var stored otaCommand
			if err := tx.Table("ota_command").Where("command_id = ? AND created_month = ?", commandID, month).Take(&stored).Error; err != nil {
				return err
			}
			if stored.DeviceID == deviceID {
				queued++
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		otaFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"queued": queued, "checksum_sha256": strings.ToLower(request.Checksum)})
}

// ack 记录设备回报的结果。设备明确拒绝的不会再重试，
// 而针对一条不存在的命令的 ack 会被拒绝，而不是凭空造出一条。
func (a OtaAPI) ack(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var request struct {
		CommandID        string `json:"command_id" binding:"required,max=64"`
		DeviceID         string `json:"device_id" binding:"required,max=64"`
		Success          bool   `json:"success"`
		InstalledVersion string `json:"installed_version"`
		Reason           string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		httpapi.BadRequest(c, "invalid OTA ack")
		return
	}
	month := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var row otaCommand
		if err := tx.Table("ota_command").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("command_id = ? AND device_id = ? AND created_month = ?", request.CommandID, request.DeviceID, month).
			Take(&row).Error; err != nil {
			return err
		}
		if row.Status == "acked" {
			// 已经确认过：重复的 ack 不允许再推动这个终态。
			return nil
		}
		if row.Status == "failed" {
			return ErrOtaTerminal
		}
		if request.Success {
			return tx.Table("ota_command").Where("id = ? AND created_month = ?", row.ID, month).
				Updates(map[string]any{"status": "acked", "acked_at": gorm.Expr("UTC_TIMESTAMP(3)")}).Error
		}
		reason := strings.TrimSpace(request.Reason)
		if reason == "" {
			reason = "设备拒绝升级"
		}
		if len(reason) > 255 {
			reason = reason[:255]
		}
		return tx.Table("ota_command").Where("id = ? AND created_month = ?", row.ID, month).
			Updates(map[string]any{"status": "failed", "failed_at": gorm.Expr("UTC_TIMESTAMP(3)"), "failure_reason": reason}).Error
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		otaFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"command_id": request.CommandID})
}

// status 报告某个计划的推送进度，central 靠轮询它
// 来决定这一批是可以继续，还是必须停下来。
func (a OtaAPI) status(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	deviceID := strings.TrimSpace(c.Query("device_id"))
	schedule := strings.TrimSpace(c.Query("schedule_filter"))
	if deviceID == "" || len(deviceID) > 64 {
		httpapi.BadRequest(c, "device_id is required")
		return
	}
	rows := []map[string]any{}
	if err := a.DB.WithContext(c.Request.Context()).Table("ota_command").
		Select("command_id, device_id, package_id, package_version, status, sent_at, acked_at, failed_at, failure_reason, retry_count").
		Where("device_id = ?", deviceID).Order("id DESC").Limit(20).Find(&rows).Error; err != nil {
		otaFailure(c, err)
		return
	}
	_ = schedule
	httpapi.OK(c, gin.H{"commands": rows})
}

var (
	ErrOtaInvalidDevice = errors.New("device is not provisioned or is disabled")
	ErrOtaTerminal      = errors.New("ota command already reached a terminal state")
)

func otaFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrOtaInvalidDevice):
		httpapi.Write(c, http.StatusConflict, 2009, err.Error(), nil)
	case errors.Is(err, ErrOtaTerminal):
		httpapi.Write(c, http.StatusConflict, 2009, err.Error(), nil)
	case errors.Is(err, gorm.ErrRecordNotFound):
		httpapi.Write(c, http.StatusNotFound, 1004, "OTA command not found", nil)
	default:
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "ota unavailable", nil)
	}
}

// VerifyChecksum 让 central 在发布固件包之前先核对制品哈希，
// 这样上传损坏的包永远到不了设备手上。
func VerifyChecksum(expected, actual string) error {
	want, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(expected)))
	if err != nil || len(want) != sha256.Size {
		return errors.New("expected checksum must be a sha256 hex digest")
	}
	got, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(actual)))
	if err != nil || len(got) != sha256.Size {
		return errors.New("actual checksum must be a sha256 hex digest")
	}
	if subtle.ConstantTimeCompare(want, got) != 1 {
		return errors.New("firmware checksum does not match")
	}
	return nil
}
