package auth

import (
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrSecretTooShort = errors.New("JWT secret must be at least 32 bytes")
	ErrInvalidToken   = errors.New("invalid or expired JWT")
)

type Claims struct {
	Subject   string
	Kind      string // user or admin
	OpenID    string
	SessionID string
	RoleIDs   []uint64
	IssuedAt  int64
	ExpiresAt int64
}

type wireClaims struct {
	Kind      string   `json:"kind"`
	OpenID    string   `json:"openid,omitempty"`
	SessionID string   `json:"sid,omitempty"`
	RoleIDs   []uint64 `json:"role_ids,omitempty"`
	jwt.RegisteredClaims
}

type JWT struct {
	key []byte
}

func NewJWT(secret string) (*JWT, error) {
	if len(secret) < 32 {
		return nil, ErrSecretTooShort
	}
	return &JWT{key: []byte(secret)}, nil
}

func (j *JWT) Sign(claims Claims) (string, error) {
	if !validClaims(claims) || claims.ExpiresAt <= claims.IssuedAt {
		return "", ErrInvalidToken
	}
	wire := wireClaims{
		Kind:      claims.Kind,
		OpenID:    claims.OpenID,
		SessionID: claims.SessionID,
		RoleIDs:   claims.RoleIDs,
		Subject:   claims.Subject,
		IssuedAt:  jwt.NewNumericDate(time.Unix(claims.IssuedAt, 0)),
		ExpiresAt: jwt.NewNumericDate(time.Unix(claims.ExpiresAt, 0)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, wire).SignedString(j.key)
}

func (j *JWT) Verify(token, expectedKind string, now time.Time) (Claims, error) {
	wire := &wireClaims{}
	parsed, err := jwt.ParseWithClaims(token, wire, func(_ *jwt.Token) (any, error) {
		return j.key, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil || !parsed.Valid || wire.IssuedAt == nil || wire.ExpiresAt == nil {
		return Claims{}, ErrInvalidToken
	}
	claims := Claims{
		Subject:   wire.Subject,
		Kind:      wire.Kind,
		OpenID:    wire.OpenID,
		SessionID: wire.SessionID,
		RoleIDs:   wire.RoleIDs,
		IssuedAt:  wire.IssuedAt.Unix(),
		ExpiresAt: wire.ExpiresAt.Unix(),
	}
	if !validClaims(claims) || claims.Kind != expectedKind || claims.IssuedAt > now.Unix()+60 {
		return Claims{}, ErrInvalidToken
	}
	return claims, nil
}

func validClaims(c Claims) bool {
	if c.Kind != "user" && c.Kind != "admin" || c.Subject == "" || c.IssuedAt <= 0 || c.ExpiresAt <= 0 {
		return false
	}
	if _, err := strconv.ParseUint(c.Subject, 10, 64); err != nil {
		return false
	}
	if c.Kind == "user" && c.OpenID == "" {
		return false
	}
	if c.Kind == "admin" && c.OpenID != "" {
		return false
	}
	return true
}
