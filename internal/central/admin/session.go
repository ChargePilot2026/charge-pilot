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

var ErrInvalidRefresh = errors.New("invalid or expired refresh token")

const refreshTTL = 7 * 24 * time.Hour

type sessionRecord struct {
	AuthVersion uint64   `json:"version"`
	AdminID     uint64   `json:"uid"`
	Username    string   `json:"username"`
	Digest      string   `json:"digest"`
	Previous    []string `json:"previous,omitempty"`
}

type Sessions struct{ Redis *redis.Client }

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

func sessionKey(sid string) string { return "admin:session:" + sid }
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

func mfaKey(token string) string { return "admin:mfa:" + token }
