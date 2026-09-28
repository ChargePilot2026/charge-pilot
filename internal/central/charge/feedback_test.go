package charge

import "testing"

func TestHTTPSImageRejectsNonTLS(t *testing.T) {
	// Feedback images are rendered back to staff in the casework queue, so a
	// non-TLS or script-bearing link is an injection vector, not a formatting
	// preference.
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
