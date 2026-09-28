package netguard

import "testing"

// These cases encode the security boundary: a subscription must never be able
// to reach the host, the cluster, or a cloud metadata service, while an ordinary
// public HTTPS endpoint must remain usable.
func TestValidatePublicHTTPSBlocksInternalTargets(t *testing.T) {
	blocked := []string{
		"https://localhost/hook",
		"https://api.localhost/hook",
		"http://example.com/hook", // not HTTPS
		"https://user:pass@example.com/hook",
		"https://127.0.0.1/hook",
		"https://127.10.20.30/hook",
		"https://[::1]/hook",
		"https://10.0.0.5/hook",
		"https://172.16.0.1/hook",
		"https://192.168.1.1/hook",
		"https://169.254.169.254/latest/meta-data/",
		"https://metadata.google.internal/computeMetadata/v1/",
		"https://service.internal/hook",
		"https://db.local/hook",
		"https://node.cluster.local/hook",
		"https://0.0.0.0/hook",
		"https://100.64.0.1/hook", // carrier-grade NAT
		"ftp://example.com/hook",
		"https://[fd00::1]/hook", // unique local IPv6
	}
	for _, target := range blocked {
		if err := ValidatePublicHTTPS(target); err == nil {
			t.Errorf("internal target accepted: %s", target)
		}
	}
}

func TestValidatePublicHTTPSAcceptsOrdinaryEndpoints(t *testing.T) {
	// Literal public addresses must not depend on DNS to be judged.
	allowed := []string{
		"https://93.184.216.34/hook",
		"https://8.8.8.8/webhook",
		"https://[2606:2800:220:1:248:1893:25c8:1946]/hook",
	}
	for _, target := range allowed {
		if err := ValidatePublicHTTPS(target); err != nil {
			t.Errorf("public target rejected: %s (%v)", target, err)
		}
	}
}

func TestValidatePublicHTTPSRejectsMalformedInput(t *testing.T) {
	for _, target := range []string{"", "not a url", "https://", "://example.com"} {
		if err := ValidatePublicHTTPS(target); err == nil {
			t.Errorf("malformed target accepted: %q", target)
		}
	}
}
