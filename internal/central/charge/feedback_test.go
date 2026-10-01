package charge

import "testing"

func TestHTTPSImageRejectsNonTLS(t *testing.T) {
	// 验证反馈附件拒绝非 HTTPS 及脚本链接，避免在客服页面回显不安全 URL。
	accepted := []string{
		"https://cdn.example.com/a.jpg",
		"https://cdn.example.com/a.jpg?x=1&y=2",
	}
	for _, raw := range accepted {
		if !httpsImage(raw) {
			t.Fatalf("%q should be accepted", raw)
		}
	}
	rejected := []string{
		"",
		"http://cdn.example.com/a.jpg",
		"//cdn.example.com/a.jpg",
		"javascript:alert(1)",
		"data:image/png;base64,AAAA",
		"https://",
		" https://cdn.example.com/a.jpg",
		"https://user:pass@cdn.example.com/a.jpg",
		"https://cdn.example.com/" + string(make([]byte, 600)),
	}
	for _, raw := range rejected {
		if httpsImage(raw) {
			t.Fatalf("%q should be rejected", raw)
		}
	}
}
