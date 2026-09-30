package charge

import "testing"

func TestHTTPSImageRejectsNonTLS(t *testing.T) {
	// 反馈图片会回显给客服看，
	// 所以一个非 TLS 或带脚本的链接是注入载体，
	// 而不只是格式偏好问题。
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
