package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestJWTSignatureExpiryAndAudience(t *testing.T) {
	j, err := NewJWT(strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	token, err := j.Sign(Claims{Subject: "42", Kind: "user", OpenID: "wx-openid", IssuedAt: now.Unix(), ExpiresAt: now.Add(15 * time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := j.Verify(token, "user", now)
	if err != nil || claims.Subject != "42" {
		t.Fatal(claims, err)
	}
	for _, input := range []struct {
		token, kind string
		when        time.Time
	}{
		{token, "admin", now},
		{token + "x", "user", now},
		{token, "user", now.Add(16 * time.Minute)},
	} {
		if _, err := j.Verify(input.token, input.kind, input.when); !errors.Is(err, ErrInvalidToken) {
			t.Fatal(input, err)
		}
	}
}
