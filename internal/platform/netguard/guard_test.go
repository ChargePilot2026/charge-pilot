package netguard

import "testing"

// 这些用例编码的是安全边界：订阅永远不能触达宿主机、集群或云元数据服务，
// 同时一个普通的公网 HTTPS 端点必须仍然可用。
func TestValidatePublicHTTPSBlocksInternalTargets(t *testing.T) {
	blocked := []string{
		"https://localhost/hook",
		"https://api.localhost/hook",
		"http://example.com/hook", // 不是 HTTPS
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
		"https://100.64.0.1/hook", // 运营商级 NAT
		"ftp://example.com/hook",
		"https://[fd00::1]/hook", // IPv6 唯一本地地址
	}
	for _, target := range blocked {
		if err := ValidatePublicHTTPS(target); err == nil {
			t.Errorf("internal target accepted: %s", target)
		}
	}
}

func TestValidatePublicHTTPSAcceptsOrdinaryEndpoints(t *testing.T) {
	// 直写的公网地址不能依赖 DNS 才能判出结果。
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
