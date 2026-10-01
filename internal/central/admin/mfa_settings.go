package admin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const mfaEnrollmentTTL = 5 * time.Minute

var (
	errMFAProof   = errors.New("密码或验证码校验失败")
	errMFAPending = errors.New("绑定已过期或已被替换，请重新开始")
	errMFAState   = errors.New("双因素认证状态已改变，请刷新后重试")
	errMFABusy    = errors.New("账号安全设置正在处理中，请稍后重试")
)

// 同一账号的登记、确认与关闭串行执行；锁租期长于整个操作的数据库与缓存超时。
var mfaCompareDelete = redis.NewScript(`if redis.call('GET',KEYS[1]) == ARGV[1] then return redis.call('DEL',KEYS[1]) end; return 0`)

type mfaSettingsInput struct {
	Action       string `json:"action"`
	Password     string `json:"password"`
	Code         string `json:"code"`
	EnrollmentID string `json:"enrollment_id"`
}

type mfaEnrollment struct {
	ID          string    `json:"id"`
	Secret      string    `json:"secret"`
	AdminID     uint64    `json:"admin_id"`
	AuthVersion uint64    `json:"auth_version"`
	SessionID   string    `json:"session_id"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type mfaSettingsResult struct {
	EnrollmentID    string `json:"enrollment_id,omitempty"`
	Secret          string `json:"secret,omitempty"`
	OTPAuthURI      string `json:"otpauth_uri,omitempty"`
	ExpiresIn       int    `json:"expires_in,omitempty"`
	MFAEnabled      bool   `json:"mfa_enabled"`
	SessionsRevoked bool   `json:"sessions_revoked"`
}

func mfaPendingKey(id uint64) string      { return fmt.Sprintf("admin:mfa-settings:pending:%d", id) }
func mfaLockKey(id uint64) string         { return fmt.Sprintf("admin:mfa-settings:lock:%d", id) }
func mfaSettingsRateKey(id uint64) string { return fmt.Sprintf("admin:mfa-settings:rate:%d", id) }

func (in mfaSettingsInput) valid() bool {
	password := in.Password != "" && len(in.Password) <= 72
	code := len(in.Code) == 6
	for _, c := range in.Code {
		if c < '0' || c > '9' {
			code = false
		}
	}
	switch in.Action {
	case "enrol":
		return password && in.Code == "" && in.EnrollmentID == ""
	case "confirm":
		return in.Password == "" && code && len(in.EnrollmentID) == 43
	case "disable":
		return password && code && in.EnrollmentID == ""
	default:
		return false
	}
}

// mfaSettings 操作人始终取当前会话，既不接受目标账号也不接受客户端提交的密钥。
func (a API) mfaSettings(c *gin.Context) {
	var in mfaSettingsInput
	if !decodeResource(c, &in) {
		return
	}
	in.Code = strings.TrimSpace(in.Code)
	if !in.valid() {
		httpapi.BadRequest(c, "请按操作提供当前密码、绑定标识和六位验证码")
		return
	}
	p := c.MustGet("admin_profile").(Profile)
	sid := c.GetString("admin_sid")
	if sid == "" {
		a.failure(c, ErrCredentials)
		return
	}
	if a.Sessions.Redis == nil || a.Store.DB == nil {
		a.failure(c, errors.New("account security unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	n, err := limitScript.Run(ctx, a.Sessions.Redis, []string{mfaSettingsRateKey(p.ID)}).Int()
	if err != nil {
		a.failure(c, err)
		return
	}
	if n > 5 {
		c.Header("Retry-After", "60")
		httpapi.Write(c, 429, 4291, "安全验证过于频繁，请稍后重试", nil)
		return
	}
	lock, err := randomPart(16)
	if err != nil {
		a.failure(c, err)
		return
	}
	acquired, err := a.Sessions.Redis.SetNX(ctx, mfaLockKey(p.ID), lock, 30*time.Second).Result()
	if err != nil {
		a.failure(c, err)
		return
	}
	if !acquired {
		a.mfaSettingsFailure(c, errMFABusy)
		return
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		_ = mfaCompareDelete.Run(cleanup, a.Sessions.Redis, []string{mfaLockKey(p.ID)}, lock).Err()
	}()
	result, err := a.applyMFASettings(ctx, p, sid, in, c.ClientIP())
	if err != nil {
		a.mfaSettingsFailure(c, err)
		return
	}
	httpapi.OK(c, result)
}

func (a API) mfaSettingsFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errMFAProof), errors.Is(err, ErrInvalidTOTP):
		httpapi.Write(c, http.StatusBadRequest, 1002, errMFAProof.Error(), nil)
	case errors.Is(err, errMFAPending), errors.Is(err, errMFAState), errors.Is(err, errMFABusy):
		httpapi.Write(c, http.StatusConflict, 2009, err.Error(), nil)
	default:
		a.failure(c, err)
	}
}

func (a API) applyMFASettings(ctx context.Context, p Profile, sid string, in mfaSettingsInput, ip string) (mfaSettingsResult, error) {
	var result mfaSettingsResult
	err := a.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account Account
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='active' AND deleted_at IS NULL", p.ID).Take(&account).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCredentials
			}
			return err
		}
		if account.AuthVersion != p.AuthVersion {
			return ErrCredentials
		}
		switch in.Action {
		case "enrol":
			if bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(in.Password)) != nil {
				return errMFAProof
			}
			if account.MFAEnabled {
				return errMFAState
			}
			secret, err := NewTOTPSecret()
			if err != nil {
				return err
			}
			id, err := randomPart(32)
			if err != nil {
				return err
			}
			uri, err := TOTPURI("ChargePilot", account.Username, secret)
			if err != nil {
				return err
			}
			pending := mfaEnrollment{ID: id, Secret: secret, AdminID: account.ID, AuthVersion: account.AuthVersion, SessionID: sid, ExpiresAt: time.Now().UTC().Add(mfaEnrollmentTTL)}
			raw, err := json.Marshal(pending)
			if err != nil {
				return err
			}
			if err := a.Sessions.Redis.Set(ctx, mfaPendingKey(account.ID), raw, mfaEnrollmentTTL).Err(); err != nil {
				return err
			}
			// 登记只写短期缓存，不改变当前 MFA，也不撤销会话。
			result = mfaSettingsResult{EnrollmentID: id, Secret: secret, OTPAuthURI: uri, ExpiresIn: int(mfaEnrollmentTTL / time.Second)}
			return nil
		case "confirm":
			if account.MFAEnabled {
				return errMFAState
			}
			raw, err := a.Sessions.Redis.Get(ctx, mfaPendingKey(account.ID)).Bytes()
			if errors.Is(err, redis.Nil) {
				return errMFAPending
			}
			if err != nil {
				return err
			}
			var pending mfaEnrollment
			if err := json.Unmarshal(raw, &pending); err != nil || pending.AdminID != account.ID || pending.AuthVersion != account.AuthVersion || pending.SessionID != sid || !time.Now().UTC().Before(pending.ExpiresAt) || subtle.ConstantTimeCompare([]byte(pending.ID), []byte(in.EnrollmentID)) != 1 {
				return errMFAPending
			}
			if err := VerifyTOTP(pending.Secret, in.Code, time.Now().UTC()); err != nil {
				return errMFAProof
			}
			// 在更新前原子消费相同缓存。坏码不消费，过期或并发替换不启用。
			consumed, err := mfaCompareDelete.Run(ctx, a.Sessions.Redis, []string{mfaPendingKey(account.ID)}, string(raw)).Int()
			if err != nil {
				return err
			}
			if consumed != 1 {
				return errMFAPending
			}
			if err := tx.Model(&Account{}).Where("id=?", account.ID).Updates(map[string]any{"mfa_secret": pending.Secret, "mfa_enabled": true, "auth_version": gorm.Expr("auth_version+1")}).Error; err != nil {
				return err
			}
			// audit 只记录账号和动作，永不持久化密钥、验证码或密码。
			if err := audit(tx, account, "mfa_enable", ip); err != nil {
				return err
			}
			result = mfaSettingsResult{MFAEnabled: true, SessionsRevoked: true}
			return nil
		case "disable":
			if bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(in.Password)) != nil {
				return errMFAProof
			}
			if !account.MFAEnabled || account.MFASecret == nil {
				return errMFAState
			}
			if err := VerifyTOTP(*account.MFASecret, in.Code, time.Now().UTC()); err != nil {
				return errMFAProof
			}
			if err := a.Sessions.Redis.Del(ctx, mfaPendingKey(account.ID)).Err(); err != nil {
				return err
			}
			if err := tx.Model(&Account{}).Where("id=?", account.ID).Updates(map[string]any{"mfa_secret": nil, "mfa_enabled": false, "auth_version": gorm.Expr("auth_version+1")}).Error; err != nil {
				return err
			}
			if err := audit(tx, account, "mfa_disable", ip); err != nil {
				return err
			}
			result = mfaSettingsResult{SessionsRevoked: true}
			return nil
		default:
			return errMFAState
		}
	})
	return result, err
}
