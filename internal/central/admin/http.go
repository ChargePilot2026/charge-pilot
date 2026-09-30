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

// API 是管理后台的鉴权与会话入口：持有账号存储、JWT 签发器和 Redis 会话，
// 登录、续期、改密、登出以及所有后台路由的权限校验都从这里出。
type API struct {
	Store    Store     // 账号与权限的持久化入口
	Sessions Sessions  // Redis 会话管理，登录态的存在与撤销都以它为准
	JWT      *auth.JWT // 后台访问令牌的签发与校验
}

// Register 挂载鉴权相关路由：OpenAPI 文档、登录、MFA 验证、令牌续期、改密、登出和"我是谁"。
// 除登录与文档外，每条路由都先过 a.Require 鉴权；Require 传空串表示"只要求登录，不额外要求权限位"。
func (a API) Register(r *gin.Engine) {
	r.GET("/api/docs/admin.openapi.json", func(c *gin.Context) { c.Data(200, "application/json; charset=utf-8", openAPIDocument) })
	r.POST("/api/v1/admin/auth/login", a.login)
	r.POST("/api/v1/admin/auth/mfa", a.verifyMFA)
	r.POST("/api/v1/admin/auth/refresh", a.refresh)
	r.POST("/api/v1/admin/auth/change-password", a.Require(""), a.changePassword)
	r.POST("/api/v1/admin/auth/logout", a.Require(""), a.logout)
	r.GET("/api/v1/admin/auth/me", a.Require(""), func(c *gin.Context) { httpapi.OK(c, c.MustGet("admin_profile")) })
}

// limitScript 在 Redis 里对同一来源 IP 的登录请求计数，首次计数时顺带设置 60 秒过期，
// 靠脚本原子完成"加一 + 判是否刚启动过期"，避免并发下窗口被反复重置。
var limitScript = redis.NewScript(`local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],60) end; return n`)

// rate 对登录类接口做按 IP 的频率限制：60 秒内超过 5 次就拒（429）并给出 Retry-After。
// Redis 不可用时按 503 失败关闭，宁可暂时登不上也不放行无限次尝试。
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

// login 账号密码登录的第一关。先过频率限制，再校验口令。
// 账号开了双因素认证时登录不算完成，只返回一个短期 challenge 让前端走 MFA；
// 没开则直接 completeLogin 发放会话与令牌。
func (a API) login(c *gin.Context) {
	if !a.rate(c) {
		return
	}
	var request struct {
		Username string `json:"username" binding:"required,max=64"` // 登录名，必填，最多 64 字符
		Password string `json:"password" binding:"required,max=72"` // 口令，必填，最多 72 字节（bcrypt 上限）
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

// verifyMFA 完成被双因素挡住的登录：用 challenge 换回账号，再用 TOTP 码校验，成功后销掉 challenge 并发放会话。
// verifyMFA finishes a login that paused for a second factor.
func (a API) verifyMFA(c *gin.Context) {
	if !a.rate(c) {
		return
	}
	var request struct {
		Challenge string `json:"mfa_challenge" binding:"required,max=128"` // 上一步 login 返回的 MFA challenge，短期有效
		Code      string `json:"code" binding:"required,max=16"`           // TOTP 验证码，最多 16 字符
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

// completeLogin 登录成功的收尾：读出账号当前的权限档案，建会话拿到 sid 与刷新令牌，再统一发给前端。
// 任一步失败都走 failure 返回对应错误，不会留下半开的会话。
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

// refresh 用刷新令牌换一对新令牌。旧刷新令牌立即作废（Rotate 里轮换），防重放。
// 若账号的 authVersion 已变（改密、角色调整导致旧令牌全部失效），就把新会话也撤销并按凭证错误返回。
func (a API) refresh(c *gin.Context) {
	if !a.rate(c) {
		return
	}
	var request struct {
		RefreshToken string `json:"refresh_token" binding:"required,max=256"` // 登录时下发的刷新令牌
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

// issue 签发 15 分钟有效的访问令牌并返回前端所需的完整登录信息（令牌、刷新令牌、角色与权限清单）。
// 令牌里带 kind="admin"、会话 sid 和角色 ID，后台与用户侧的令牌因此不能互相通用。
func (a API) issue(c *gin.Context, p Profile, sid, refresh string) {
	now := time.Now()
	token, err := a.JWT.Sign(auth.Claims{Subject: strconv.FormatUint(p.ID, 10), Kind: "admin", SessionID: sid, RoleIDs: []uint64{p.RoleID}, IssuedAt: now.Unix(), ExpiresAt: now.Add(15 * time.Minute).Unix()})
	if err != nil {
		a.failure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"token": token, "access_token": token, "refresh_token": refresh, "expires_in": 900, "admin_user_id": p.ID, "username": p.Username, "role": p.Role, "permissions": p.Permissions})
}

// Require 返回鉴权中间件：校验 Bearer 令牌、Redis 会话是否还在、档案与令牌是否匹配，
// 再按 permission 检查权限位（传空串只要求登录）。通过后把档案和 sid 放进上下文供后续处理函数使用。
// 任何一环不通过都直接写响应并 Abort，权限不足与凭证无效的提示是分开的两条。
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

// logout 主动登出：按上下文里的 sid 直接删掉 Redis 会话，删掉后所有用该 sid 签发的令牌立即失效。
func (a API) logout(c *gin.Context) {
	if err := a.Sessions.Redis.Del(c.Request.Context(), sessionKey(c.GetString("admin_sid"))).Err(); err != nil {
		a.failure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"revoked": true})
}

// failure 把鉴权/账号类错误翻译成统一的 HTTP 状态与业务码：凭证类一律 401，账号锁定 423，TOTP 或 MFA 挑战无效 401，
// 其余（含 Redis 故障）按 503 服务不可用处理，不向调用方泄露内部细节。
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

// changePassword 后台账号自助改密。要求新旧密码不同、新密码 12–72 字节；
// 改密由存储层连带提升 authVersion，因此该账号在其他设备上的登录态会一起失效。
func (a API) changePassword(c *gin.Context) {
	var request struct {
		OldPassword string `json:"old_password" binding:"required,max=72"`        // 旧口令，必填
		NewPassword string `json:"new_password" binding:"required,min=12,max=72"` // 新口令，长度 12–72 字节且不得与旧口令相同
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
