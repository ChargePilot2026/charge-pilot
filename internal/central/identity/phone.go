package identity

import (
	"context"
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/phone"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

var errPhoneTaken = errors.New("手机号已被占用")

// PhoneAPI 提供 C 端手机号绑定/解绑接口。user 表归 identity 家族，
// 手机号列的写入与登录档案同包维护；正式绑定必须使用微信授权凭证，
// 开发模式的明文号码仅供联调。
type PhoneAPI struct {
	Auth             SessionAuthenticator
	DB               *gorm.DB
	DevelopmentPhone bool
	PhoneExchange    func(context.Context, string) (string, error)
}

func (a PhoneAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/user/phone/bind", a.bind)
	r.POST("/api/v1/user/phone/unbind", a.unbind)
}

func (a PhoneAPI) bind(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "手机号请求无效")
		return
	}
	if !a.DevelopmentPhone {
		if in.Code == "" || in.Phone != "" {
			httpapi.BadRequest(c, "请使用微信手机号授权凭证")
			return
		}
		if a.PhoneExchange == nil {
			httpapi.Write(c, 503, 5003, "手机号授权暂不可用", nil)
			return
		}
		phone, err := a.PhoneExchange(c.Request.Context(), in.Code)
		if err != nil {
			httpapi.Write(c, 400, 1004, "手机号授权失败，请重新授权", nil)
			return
		}
		in.Phone = phone
	}
	in.Phone = phone.Normalize(in.Phone)
	if !phone.Valid(in.Phone) {
		httpapi.BadRequest(c, "请输入有效的中国大陆手机号")
		return
	}
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// 查询提供明确的冲突提示，唯一索引兜底并发绑定。
		var holder int64
		if err := tx.Table("user").Where("phone = ? AND id <> ? AND deleted_at IS NULL", in.Phone, userID).Count(&holder).Error; err != nil {
			return err
		}
		if holder > 0 {
			return errPhoneTaken
		}
		return tx.Table("user").Where("id = ? AND deleted_at IS NULL", userID).
			Updates(map[string]any{"phone": in.Phone}).Error
	})
	var duplicate *mysql.MySQLError
	switch {
	case errors.Is(err, errPhoneTaken) || errors.As(err, &duplicate) && duplicate.Number == 1062:
		httpapi.Write(c, 409, 2009, "该手机号已绑定其他账号", nil)
	case err != nil:
		httpapi.Write(c, 503, 5003, "数据暂时无法保存，请稍后重试", nil)
	default:
		httpapi.OK(c, gin.H{"bound": true, "phone_masked": phone.Mask(in.Phone)})
	}
}

func (a PhoneAPI) unbind(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	err := a.DB.WithContext(c.Request.Context()).Table("user").
		Where("id = ? AND deleted_at IS NULL", userID).
		Updates(map[string]any{"phone": nil}).Error
	if err != nil {
		httpapi.Write(c, 503, 5003, "数据暂时无法保存，请稍后重试", nil)
		return
	}
	httpapi.OK(c, gin.H{"bound": false, "unbound": true})
}
