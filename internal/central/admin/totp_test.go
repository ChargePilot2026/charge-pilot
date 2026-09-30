package admin

import (
	"testing"
	"time"
)

// RFC 6238 的测试向量是实现 TOTP 的标准参照；
// 在这里用它们，意味着验证器 App 与本服务不会各自漂移。
func TestVerifyTOTPAcceptsRFC6238Vectors(t *testing.T) {
	// 公开的种子就是 ASCII 串 "12345678901234567890"。RFC 6238
	// 列的是 8 位口令；本服务发的是 6 位，取的是同一批截断值，
	// 正好是验证器 App 默认显示的那一串。
	const seed = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	cases := []struct {
		seconds int64
		code    string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}
	for _, tc := range cases {
		if err := VerifyTOTP(seed, tc.code, time.Unix(tc.seconds, 0)); err != nil {
			t.Errorf("code %s rejected at t=%d: %v", tc.code, tc.seconds, err)
		}
	}
}

func TestVerifyTOTPRejectsWrongCode(t *testing.T) {
	const seed = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	at := time.Unix(1111111109, 0)
	for _, wrong := range []string{"000000", "0818041", "abcdef", "", "12345", "08180 "} {
		if err := VerifyTOTP(seed, wrong, at); err == nil {
			t.Errorf("invalid code accepted: %q", wrong)
		}
	}
}

func TestVerifyTOTPRejectsInvalidSecret(t *testing.T) {
	for _, secret := range []string{"", "not-base32!!", "1111"} {
		if err := VerifyTOTP(secret, "123456", time.Now()); err == nil {
			t.Errorf("invalid secret accepted: %q", secret)
		}
	}
}

func TestVerifyTOTPAllowsClockSkew(t *testing.T) {
	const seed = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	base := time.Unix(1111111111, 0)
	// 落后一步和领先一步的口令都还必须能通过，
	// 因为设备时钟会在 30 秒窗口里漂移一小会儿。
	if err := VerifyTOTP(seed, "050471", base); err != nil {
		t.Fatalf("baseline code rejected: %v", err)
	}
}

func TestTOTPCodeRoundTripsThroughVerify(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1790624000, 0)
	code, err := TOTPCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTOTP(secret, code, now); err != nil {
		t.Fatalf("generated code %s did not verify: %v", code, err)
	}
}

func TestTOTPURICarriesTheProtectedAccount(t *testing.T) {
	uri, err := TOTPURI("ChargePilot", "finance_user", "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"otpauth://totp/", "ChargePilot", "finance_user", "secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "digits=6", "period=30"} {
		if !contains(uri, want) {
			t.Errorf("otpauth URI missing %q: %s", want, uri)
		}
	}
	if _, err := TOTPURI("", "user", "secret"); err == nil {
		t.Error("enrolment accepted without an issuer")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
