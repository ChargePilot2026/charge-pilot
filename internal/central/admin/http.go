package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

type API struct {
	Store    Store
	Sessions Sessions
	JWT      *auth.JWT
}

func (a API) Register(r *gin.Engine) {
	r.GET("/api/docs/admin.openapi.json", func(c *gin.Context) { c.Data(200, "application/json; charset=utf-8", openAPIDocument) })
	r.POST("/api/v1/admin/auth/login", a.login)
	r.POST("/api/v1/admin/auth/mfa", a.verifyMFA)
	r.POST("/api/v1/admin/auth/refresh", a.refresh)
	r.POST("/api/v1/admin/auth/change-password", a.Require(""), a.changePassword)
	r.POST("/api/v1/admin/auth/logout", a.Require(""), a.logout)
	r.GET("/api/v1/admin/auth/me", a.Require(""), func(c *gin.Context) { httpapi.OK(c, c.MustGet("admin_profile")) })
}

var limitScript = redis.NewScript(`local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],60) end; return n`)

func (a API) rate(c *gin.Context) bool {
	n, err := limitScript.Run(c.Request.Context(), a.Sessions.Redis, []string{"admin:login-rate:" + c.ClientIP()}).Int()
	if err != nil {
		httpapi.Write(c, 503, 5003, "登录服务暂不可用", nil)
		return false
	}
	if n > 5 {
		c.Header("Retry-After", "60")
		httpapi.Write(c, 429, 4291, "登录过于频繁，请稍后重试", nil)
		return false
	}
	return true
}
func (a API) login(c *gin.Context) {
	if !a.rate(c) {
		return
	}
	var request struct {
		Username string `json:"username" binding:"required,max=64"`
		Password string `json:"password" binding:"required,max=72"`
	}
	if c.ShouldBindJSON(&request) != nil {
		httpapi.BadRequest(c, "请输入用户名和密码")
		return
	}
	account, err := a.Store.Login(c.Request.Context(), request.Username, request.Password, c.ClientIP())
	if err != nil {
		a.failure(c, err)
		return
	}
	if account.MFAEnabled {
		// The password step succeeded but the login is not complete. Hand back a
		// short-lived challenge instead of a session token.
		challenge, err := a.Sessions.BeginMFA(c.Request.Context(), account)
		if err != nil {
			a.failure(c, err)
			return
		}
		httpapi.OK(c, gin.H{"mfa_required": true, "mfa_challenge": challenge})
		return
	}
	a.completeLogin(c, account)
}

// verifyMFA finishes a login that paused for a second factor.
func (a API) verifyMFA(c *gin.Context) {
	if !a.rate(c) {
		return
	}
	var request struct {
		Challenge string `json:"mfa_challenge" binding:"required,max=128"`
		Code      string `json:"code" binding:"required,max=16"`
	}
	if c.ShouldBindJSON(&request) != nil {
		httpapi.BadRequest(c, "请输入验证码")
		return
	}
	accountID, err := a.Sessions.ResolveMFA(c.Request.Context(), request.Challenge)
	if err != nil {
		a.failure(c, err)
		return
	}
	account, err := a.Store.CompleteMFA(c.Request.Context(), accountID, request.Code, c.ClientIP())
	if err != nil {
		a.failure(c, err)
		return
	}
	_ = a.Sessions.Revoke(c.Request.Context(), request.Challenge)
	a.completeLogin(c, account)
}

func (a API) completeLogin(c *gin.Context, account Account) {
	profile, err := a.Store.Profile(c.Request.Context(), account.ID)
	if err != nil {
		a.failure(c, err)
		return
	}
	sid, refresh, err := a.Sessions.Create(c.Request.Context(), account)
	if err != nil {
		a.failure(c, err)
		return
	}
	a.issue(c, profile, sid, refresh)
}
func (a API) refresh(c *gin.Context) {
	if !a.rate(c) {
		return
	}
	var request struct {
		RefreshToken string `json:"refresh_token" binding:"required,max=256"`
	}
	if c.ShouldBindJSON(&request) != nil {
		httpapi.BadRequest(c, "invalid refresh token")
		return
	}
	account, sid, refresh, err := a.Sessions.Rotate(c.Request.Context(), request.RefreshToken)
	if err != nil {
		a.failure(c, err)
		return
	}
	profile, err := a.Store.Profile(c.Request.Context(), account.ID)
	if err != nil {
		_ = a.Sessions.Revoke(c.Request.Context(), refresh)
		a.failure(c, err)
		return
	}
	if account.AuthVersion != profile.AuthVersion {
		_ = a.Sessions.Revoke(c.Request.Context(), refresh)
		a.failure(c, ErrCredentials)
		return
	}
	a.issue(c, profile, sid, refresh)
}
func (a API) issue(c *gin.Context, p Profile, sid, refresh string) {
	now := time.Now()
	token, err := a.JWT.Sign(auth.Claims{Subject: strconv.FormatUint(p.ID, 10), Kind: "admin", SessionID: sid, RoleIDs: []uint64{p.RoleID}, IssuedAt: now.Unix(), ExpiresAt: now.Add(15 * time.Minute).Unix()})
	if err != nil {
		a.failure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"token": token, "access_token": token, "refresh_token": refresh, "expires_in": 900, "admin_user_id": p.ID, "username": p.Username, "role": p.Role, "permissions": p.Permissions})
}
func (a API) Require(permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if !strings.HasPrefix(raw, "Bearer ") {
			a.failure(c, ErrCredentials)
			c.Abort()
			return
		}
		claims, err := a.JWT.Verify(strings.TrimPrefix(raw, "Bearer "), "admin", time.Now())
		if err != nil {
			a.failure(c, ErrCredentials)
			c.Abort()
			return
		}
		exists, err := a.Sessions.Exists(c.Request.Context(), claims.SessionID)
		if err != nil {
			a.failure(c, err)
			c.Abort()
			return
		}
		if !exists {
			a.failure(c, ErrCredentials)
			c.Abort()
			return
		}
		id, _ := strconv.ParseUint(claims.Subject, 10, 64)
		p, err := a.Store.Profile(c.Request.Context(), id)
		if err != nil {
			a.failure(c, err)
			c.Abort()
			return
		}
		matches, err := a.Sessions.Matches(c.Request.Context(), claims.SessionID, p.ID, p.AuthVersion)
		if err != nil {
			a.failure(c, err)
			c.Abort()
			return
		}
		if !matches {
			a.failure(c, ErrCredentials)
			c.Abort()
			return
		}
		if permission != "" {
			found := false
			for _, code := range p.Permissions {
				if code == permission {
					found = true
					break
				}
			}
			if !found {
				httpapi.Write(c, 403, 1003, "没有操作权限", nil)
				c.Abort()
				return
			}
		}
		c.Set("admin_profile", p)
		c.Set("admin_sid", claims.SessionID)
		c.Next()
	}
}
func (a API) logout(c *gin.Context) {
	if err := a.Sessions.Redis.Del(c.Request.Context(), sessionKey(c.GetString("admin_sid"))).Err(); err != nil {
		a.failure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"revoked": true})
}
func (a API) failure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrCredentials), errors.Is(err, ErrInvalidRefresh):
		httpapi.Write(c, http.StatusUnauthorized, 1001, "用户名或密码错误，或登录已失效", nil)
	case errors.Is(err, ErrLocked):
		httpapi.Write(c, 423, 2006, err.Error(), nil)
	case errors.Is(err, ErrInvalidTOTP):
		httpapi.Write(c, http.StatusUnauthorized, 1001, "验证码无效或已过期", nil)
	case errors.Is(err, ErrInvalidMFAChallenge):
		httpapi.Write(c, http.StatusUnauthorized, 1001, "登录已失效，请重新登录", nil)
	default:
		httpapi.Write(c, 503, 5003, "登录服务暂不可用", nil)
	}
}

func (a API) changePassword(c *gin.Context) {
	var request struct {
		OldPassword string `json:"old_password" binding:"required,max=72"`
		NewPassword string `json:"new_password" binding:"required,min=12,max=72"`
	}
	if c.ShouldBindJSON(&request) != nil || len(request.NewPassword) < 12 || len(request.NewPassword) > 72 || request.OldPassword == request.NewPassword {
		httpapi.BadRequest(c, "新密码须与旧密码不同且长度为 12–72 字节")
		return
	}
	p := c.MustGet("admin_profile").(Profile)
	if err := a.Store.ChangePassword(c.Request.Context(), p.ID, request.OldPassword, request.NewPassword, c.ClientIP()); err != nil {
		a.failure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"changed": true, "sessions_revoked": true})
}
