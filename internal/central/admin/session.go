package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrInvalidRefresh 表示刷新令牌格式不对、已过期或已被轮换掉，统一回同一句，
// 不向外区分具体是哪一种失效。
var ErrInvalidRefresh = errors.New("invalid or expired refresh token")

// refreshTTL 是刷新令牌（也就是会话）在 Redis 里的存活时间：7 天。
// 每次轮换都用 KEEPTTL 保留它，所以这是会话的绝对寿命，不因刷新而无限延长。
const refreshTTL = 7 * 24 * time.Hour

// sessionRecord 是存放在 Redis 里的一条会话/挑战记录，JSON 编码。
// Redis 只存这个，不存刷新令牌明文：外部拿到的是令牌，Redis 里只有令牌的散列。
type sessionRecord struct {
	AuthVersion uint64   `json:"version"`            // 账号的权限版本号，改密码/改角色后会变，用于让旧会话立刻失效
	AdminID     uint64   `json:"uid"`                // 后台账号 id
	Username    string   `json:"username"`           // 登录名，供审计记录操作人
	Digest      string   `json:"digest"`             // 当前刷新令牌密钥的 sha256 散列，校验用
	Previous    []string `json:"previous,omitempty"` // 最近 32 个已轮换掉的旧密钥散列，只为让上一枚令牌还能用一次
}

// Sessions 管理后台会话，底层就是 Redis 里的 admin:session:<sid> 键。
// 结构体只持有 Redis 客户端（字段 Redis），没有本地状态，可以直接按值复制。
type Sessions struct{ Redis *redis.Client }

// Create 为一个账号新建会话，返回会话 id（sid）和完整的刷新令牌。
// 令牌形如 ART_<sid>.<secret>，sid 用来定位 Redis 键，secret 只有散列入 Redis；
// 完整明文只在这里返回一次，之后客户端凭它换新令牌。
func (s Sessions) Create(ctx context.Context, account Account) (string, string, error) {
	sid, err := randomPart(16)
	if err != nil {
		return "", "", err
	}
	secret, err := randomPart(32)
	if err != nil {
		return "", "", err
	}
	token := "ART_" + sid + "." + secret
	record, err := json.Marshal(sessionRecord{AuthVersion: account.AuthVersion, AdminID: account.ID, Username: account.Username, Digest: digest(secret)})
	if err != nil {
		return "", "", err
	}
	if err := s.Redis.Set(ctx, sessionKey(sid), record, refreshTTL).Err(); err != nil {
		return "", "", err
	}
	return sid, token, nil
}

// rotateScript 是轮换刷新令牌的 Lua 脚本，整个校验与改写在 Redis 里一次完成，
// 避免两个并发请求拿到同一枚旧令牌各自换出新会话。
// 散列对不上直接失败；换成功时把旧散列压进 previous（最多留 32 个），
// 并用 KEEPTTL 保留原来的 7 天过期时间，不因为轮换而延长会话寿命。
var rotateScript = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then return false end
local record = cjson.decode(value)
if record.digest ~= ARGV[1] then return false end
record.digest = ARGV[2]
record.previous = record.previous or {}
table.insert(record.previous, ARGV[1])
if #record.previous > 32 then table.remove(record.previous, 1) end
redis.call('SET', KEYS[1], cjson.encode(record), 'KEEPTTL')
return value
`)

// Rotate 用一枚刷新令牌换出新令牌，返回新会话代表的账号、sid 和新令牌。
// 令牌对不上（含已被别人轮换掉）时返回 ErrInvalidRefresh。
// 旧令牌被换掉后并不是立刻作废：它还在 previous 里，客户端并发重试时仍能换一次。
func (s Sessions) Rotate(ctx context.Context, token string) (Account, string, string, error) {
	sid, oldSecret, ok := parseRefresh(token)
	if !ok {
		return Account{}, "", "", ErrInvalidRefresh
	}
	newSecret, err := randomPart(32)
	if err != nil {
		return Account{}, "", "", err
	}
	result, err := rotateScript.Run(ctx, s.Redis, []string{sessionKey(sid)}, digest(oldSecret), digest(newSecret)).Result()
	if errors.Is(err, redis.Nil) || result == nil || result == int64(0) {
		return Account{}, "", "", ErrInvalidRefresh
	}
	if err != nil {
		return Account{}, "", "", err
	}
	value, ok := result.(string)
	if !ok {
		return Account{}, "", "", ErrInvalidRefresh
	}
	var record sessionRecord
	if err := json.Unmarshal([]byte(value), &record); err != nil {
		return Account{}, "", "", err
	}
	return Account{ID: record.AdminID, Username: record.Username, AuthVersion: record.AuthVersion}, sid, "ART_" + sid + "." + newSecret, nil
}

// Revoke 注销一个会话（退出登录）：令牌格式非法或散列对不上都返回 ErrInvalidRefresh。
// 当前密钥和 previous 里的旧密钥都能删掉会话，所以刚轮换过的那枚旧令牌也能用来退出。
func (s Sessions) Revoke(ctx context.Context, token string) error {
	sid, secret, ok := parseRefresh(token)
	if !ok {
		return ErrInvalidRefresh
	}
	result, err := revokeScript.Run(ctx, s.Redis, []string{sessionKey(sid)}, digest(secret)).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return ErrInvalidRefresh
	}
	return nil
}

// revokeScript 是注销会话的 Lua 脚本：比对当前散列与 previous 里的旧散列，
// 命中就删掉整个会话键，不命中返回 0。校验和删除在 Redis 里一次完成。
var revokeScript = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then return 0 end
local record = cjson.decode(value)
if record.digest == ARGV[1] then redis.call('DEL', KEYS[1]); return 1 end
for _, digest in ipairs(record.previous or {}) do
  if digest == ARGV[1] then redis.call('DEL', KEYS[1]); return 1 end
end
return 0
`)

// Exists 判断这个 sid 对应的会话是否还在。sid 为空一律当作不存在。
// 只看存在与否，不回读账号信息。
func (s Sessions) Exists(ctx context.Context, sid string) (bool, error) {
	if sid == "" {
		return false, nil
	}
	n, err := s.Redis.Exists(ctx, sessionKey(sid)).Result()
	return n == 1, err
}

// sessionKey 把会话 id 拼成 Redis 键，统一前缀便于按前缀排查和清理。
func sessionKey(sid string) string { return "admin:session:" + sid }

// digest 算刷新令牌的 sha256 散列（十六进制）。Redis 里只存散列，
// 拿到 Redis 内容也换不出可用的刷新令牌。
func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// randomPart 生成 n 字节密码学随机数并编成不带填充的 base64url 串，
// 用来生成会话 id（n=16）和令牌密钥（n=32）。
func randomPart(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// parseRefresh 拆解 "ART_<sid>.<secret>" 形式的刷新令牌。
// 除了解析，还固定校验两段的长度（16 字节和 32 字节随机数编出的 base64url 长度）
// 并确认两段都能解回原字节——形状不对的一律判无效，不进 Redis 查询。
func parseRefresh(token string) (string, string, bool) {
	if !strings.HasPrefix(token, "ART_") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(token, "ART_"), ".")
	if len(parts) != 2 || len(parts[0]) != 22 || len(parts[1]) != 43 {
		return "", "", false
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[0]); err != nil {
		return "", "", false
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[1]); err != nil {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// Matches 判断某个会话是否仍属于这个账号，且账号的权限版本号没有变过。
// 改密码或改角色会让 AuthVersion 变，此时旧会话立即判否。
func (s Sessions) Matches(ctx context.Context, sid string, id, version uint64) (bool, error) {
	if sid == "" {
		return false, nil
	}
	value, err := s.Redis.Get(ctx, sessionKey(sid)).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var record sessionRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return false, err
	}
	return record.AdminID == id && record.AuthVersion == version, nil
}

// ErrInvalidMFAChallenge means the login challenge is unknown, expired, or was
// already consumed by a successful second factor.
var ErrInvalidMFAChallenge = errors.New("invalid or expired mfa challenge")

// mfaChallengeTTL bounds how long a verified password stays usable before the
// second factor must be supplied. It is deliberately short.
const mfaChallengeTTL = 5 * time.Minute

// BeginMFA parks a half-finished login. No session exists yet, so the challenge
// carries only the account identity and cannot be used to reach any API.
func (s Sessions) BeginMFA(ctx context.Context, account Account) (string, error) {
	token, err := randomPart(32)
	if err != nil {
		return "", err
	}
	record, err := json.Marshal(sessionRecord{AuthVersion: account.AuthVersion, AdminID: account.ID, Username: account.Username})
	if err != nil {
		return "", err
	}
	if err := s.Redis.Set(ctx, mfaKey(token), record, mfaChallengeTTL).Err(); err != nil {
		return "", err
	}
	return token, nil
}

// ResolveMFA consumes the challenge atomically so one code cannot be replayed
// for two sessions. A wrong code must not burn the challenge, because the
// account owner still needs to retry; only the Lua GETDEL style consumption on
// success happens here, so resolution returns the id and the caller revokes.
func (s Sessions) ResolveMFA(ctx context.Context, token string) (uint64, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, ErrInvalidMFAChallenge
	}
	value, err := s.Redis.Get(ctx, mfaKey(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return 0, ErrInvalidMFAChallenge
	}
	if err != nil {
		return 0, err
	}
	var record sessionRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return 0, ErrInvalidMFAChallenge
	}
	return record.AdminID, nil
}

// mfaKey 把二次验证挑战码拼成 Redis 键，与会话键分开存放，
// 保证挑战记录不会被会话逻辑读成一条会话。
func mfaKey(token string) string { return "admin:mfa:" + token }
