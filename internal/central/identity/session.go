package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrInvalidRefresh = errors.New("invalid or expired refresh token")

const refreshTTL = 7 * 24 * time.Hour

type sessionRecord struct {
	UserID   sessionUserID `json:"uid"`
	OpenID   string        `json:"openid"`
	Digest   string        `json:"digest"`
	Previous []string      `json:"previous,omitempty"`
}

// Redis Lua cjson represents numbers as doubles. Keep user IDs as decimal strings,
// while accepting the integer JSON values written by earlier versions.
type sessionUserID uint64

func (id sessionUserID) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatUint(uint64(id), 10))
}

func (id *sessionUserID) UnmarshalJSON(raw []byte) error {
	value := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
	}
	if value == "" {
		return ErrInvalidRefresh
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return ErrInvalidRefresh
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return ErrInvalidRefresh
	}
	*id = sessionUserID(parsed)
	return nil
}

type Sessions struct{ Redis *redis.Client }

func (s Sessions) Create(ctx context.Context, user User) (string, string, error) {
	sid, err := randomPart(16)
	if err != nil {
		return "", "", err
	}
	secret, err := randomPart(32)
	if err != nil {
		return "", "", err
	}
	token := "RT_" + sid + "." + secret
	record, err := json.Marshal(sessionRecord{UserID: sessionUserID(user.ID), OpenID: user.OpenID, Digest: digest(secret)})
	if err != nil {
		return "", "", err
	}
	if err := s.Redis.Set(ctx, sessionKey(sid), record, refreshTTL).Err(); err != nil {
		return "", "", err
	}
	return sid, token, nil
}

var rotateScript = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then return false end
local record = cjson.decode(value)
if record.digest ~= ARGV[1] then return false end
if type(record.uid) == 'number' then
  -- Read the original decimal digits, before cjson can round a legacy ID.
  local uid = string.match(value, '"uid"%s*:%s*(%d+)%s*[,}]')
  if not uid then return false end
  record.uid = uid
end
record.digest = ARGV[2]
record.previous = record.previous or {}
table.insert(record.previous, ARGV[1])
if #record.previous > 32 then table.remove(record.previous, 1) end
redis.call('SET', KEYS[1], cjson.encode(record), 'KEEPTTL')
return value
`)

func (s Sessions) Rotate(ctx context.Context, token string) (User, string, string, error) {
	sid, oldSecret, ok := parseRefresh(token)
	if !ok {
		return User{}, "", "", ErrInvalidRefresh
	}
	newSecret, err := randomPart(32)
	if err != nil {
		return User{}, "", "", err
	}
	result, err := rotateScript.Run(ctx, s.Redis, []string{sessionKey(sid)}, digest(oldSecret), digest(newSecret)).Result()
	if errors.Is(err, redis.Nil) || result == nil || result == int64(0) {
		return User{}, "", "", ErrInvalidRefresh
	}
	if err != nil {
		return User{}, "", "", err
	}
	value, ok := result.(string)
	if !ok {
		return User{}, "", "", ErrInvalidRefresh
	}
	var record sessionRecord
	if err := json.Unmarshal([]byte(value), &record); err != nil {
		return User{}, "", "", err
	}
	return User{ID: uint64(record.UserID), OpenID: record.OpenID}, sid, "RT_" + sid + "." + newSecret, nil
}

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

func (s Sessions) Exists(ctx context.Context, sid string) (bool, error) {
	if sid == "" {
		return false, nil
	}
	n, err := s.Redis.Exists(ctx, sessionKey(sid)).Result()
	return n == 1, err
}

func sessionKey(sid string) string { return "user:session:" + sid }
func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
func randomPart(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func parseRefresh(token string) (string, string, bool) {
	if !strings.HasPrefix(token, "RT_") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(token, "RT_"), ".")
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
