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

// refreshTTL 定义会话的 7 天绝对寿命；刷新轮换使用 KEEPTTL，不延长过期时间。
const refreshTTL = 7 * 24 * time.Hour

// sessionRecord 是 Redis 中以 JSON 保存的会话或挑战记录。
// 刷新令牌仅保存摘要，不保存明文。
type sessionRecord struct {
	AuthVersion uint64   `json:"version"`            // 账号的权限版本号，改密码/改角色后会变，用于让旧会话立刻失效
	AdminID     uint64   `json:"uid"`                // 后台账号 id
	Username    string   `json:"username"`           // 登录名，供审计记录操作人
	Digest      string   `json:"digest"`             // 当前刷新令牌密钥的 sha256 散列，校验用
	Previous    []string `json:"previous,omitempty"` // 最近 32 个已轮换掉的旧密钥散列，只为让上一枚令牌还能用一次
}

// Sessions 管理后台会话，底层就是 Redis 里的 admin：session：<sid> 键。
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

// rotateScript 原子校验并轮换刷新令牌摘要，防止并发操作覆盖会话。
// 保留最多 32 个旧摘要以支持并发重试，并以 KEEPTTL 保持绝对过期时间。
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

// Rotate 校验当前或 previous 中的刷新令牌摘要，返回账号、SID 与新刷新令牌。
// 未匹配任何有效摘要时返回 ErrInvalidRefresh。
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

// Revoke 校验令牌格式及当前或 previous 摘要后删除会话；验证失败返回 ErrInvalidRefresh。
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

// Exists 判断非空 sid 对应的会话是否存在，不加载账号信息。
func (s Sessions) Exists(ctx context.Context, sid string) (bool, error) {
	if sid == "" {
		return false, nil
	}
	n, err := s.Redis.Exists(ctx, sessionKey(sid)).Result()
	return n == 1, err
}

// sessionKey 把会话 id 拼成 Redis 键，统一前缀便于按前缀排查和清理。
func sessionKey(sid string) string { return "admin:session:" + sid }

// digest 返回刷新令牌的 SHA-256 十六进制摘要，供 Redis 校验使用。
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

// Matches 校验会话所属账号及权限版本；改密或改角色提升 AuthVersion，使旧会话失效。
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

// ErrInvalidMFAChallenge 表示登录挑战不存在、已经过期，
// 或者已经被一次成功的二次验证用掉了。
var ErrInvalidMFAChallenge = errors.New("invalid or expired mfa challenge")

// mfaChallengeTTL 限制密码验证通过后完成二次验证的时间窗口。
const mfaChallengeTTL = 5 * time.Minute

// BeginMFA 创建仅含账号身份的短期登录挑战；挑战不能代替 API 会话。
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

// ResolveMFA 仅在校验成功时原子消费登录挑战并返回账号 ID。
// 验证码错误时保留挑战以允许重试，不直接创建会话。
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
