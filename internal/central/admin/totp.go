package admin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP implements RFC 6238 time-based one-time passwords over RFC 4226 HOTP.
// Implementing it locally keeps the admin login path free of an extra network
// dependency and makes the verification window explicit and testable.

const (
	totpPeriod    = 30 * time.Second
	totpDigits    = 6
	totpSkew      = 1 // accept the previous and next step for clock drift
	totpSecretLen = 20
)

var (
	// ErrInvalidTOTP means the supplied code did not match any accepted step.
	ErrInvalidTOTP = errors.New("验证码无效或已过期")
	base32Encoding = base32.StdEncoding.WithPadding(base32.NoPadding)
)

// NewTOTPSecret returns a base32 secret suitable for an authenticator app.
func NewTOTPSecret() (string, error) {
	raw := make([]byte, totpSecretLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base32Encoding.EncodeToString(raw), nil
}

// TOTPURI builds the otpauth:// enrolment URI an operator scans once.
func TOTPURI(issuer, account, secret string) (string, error) {
	if issuer == "" || account == "" || secret == "" {
		return "", errors.New("issuer, account and secret are required")
	}
	query := url.Values{}
	query.Set("secret", secret)
	query.Set("issuer", issuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", fmt.Sprint(totpDigits))
	query.Set("period", fmt.Sprint(int(totpPeriod/time.Second)))
	return (&url.URL{
		Scheme:   "otpauth",
		Host:     "totp",
		Path:     "/" + issuer + ":" + account,
		RawQuery: query.Encode(),
	}).String(), nil
}

// VerifyTOTP checks a code against the shared secret. Comparison is
// constant-time and every accepted step is tried, so a wrong code cannot be
// distinguished from a wrong step by timing.
func VerifyTOTP(secret, code string, now time.Time) error {
	secret = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return ErrInvalidTOTP
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return ErrInvalidTOTP
		}
	}
	key, err := base32Encoding.DecodeString(secret)
	if err != nil || len(key) == 0 {
		return ErrInvalidTOTP
	}
	step := now.Unix() / int64(totpPeriod/time.Second)
	var matched bool
	for offset := -totpSkew; offset <= totpSkew; offset++ {
		expected, err := hotp(key, uint64(step+int64(offset)))
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			matched = true
		}
	}
	if !matched {
		return ErrInvalidTOTP
	}
	return nil
}

// hotp derives the counter-based code defined by RFC 4226.
func hotp(key []byte, counter uint64) (string, error) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	if _, err := mac.Write(buf[:]); err != nil {
		return "", err
	}
	sum := mac.Sum(nil)
	// Dynamic truncation per RFC 4226 section 5.3.
	offset := sum[len(sum)-1] & 0x0f
	value := int64(sum[offset]&0x7f)<<24 | int64(sum[offset+1])<<16 | int64(sum[offset+2])<<8 | int64(sum[offset+3])
	mod := int32(value % 1_000_000)
	return fmt.Sprintf("%0*d", totpDigits, mod), nil
}

// TOTPCode exposes the current code, which the enrolment response needs so an
// operator can confirm the authenticator works before enabling MFA.
func TOTPCode(secret string, now time.Time) (string, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	key, err := base32Encoding.DecodeString(normalized)
	if err != nil {
		return "", err
	}
	return hotp(key, uint64(now.Unix()/int64(totpPeriod/time.Second)))
}
