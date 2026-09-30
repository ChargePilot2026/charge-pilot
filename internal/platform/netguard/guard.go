// Package netguard 拒掉那些会让调用方触达平台不应暴露的基础设施的出站目标，
// 比如回环网卡或云厂商的元数据端点。
// 它放在 platform 里，是因为 central 与 worker
// 都必须执行完全相同的这条规则。
package netguard

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// ErrTargetBlocked 标记一个会让运维触达平台不应暴露的基础设施的订阅端点。
// 它在创建订阅时拦一道，每次投递前再拦一道，
// 因为一个主机名可能稍后才开始解析到内网地址。
var ErrTargetBlocked = errors.New("订阅地址指向非公网目标，已拒绝")

// ValidatePublicHTTPS 拒掉一切不是公网 HTTPS 的目标。
// 在创建时拦下，是为了让内网目标根本没机会进表；
// 在投递时再查一遍，则堵住了 DNS 重绑定这一种情况。
func ValidatePublicHTTPS(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ErrTargetBlocked
	}
	// URL 里的 userinfo 是典型的凭据泄露写法，
	// 在 webhook 目标里没有存在的余地。
	if parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return ErrTargetBlocked
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateAddress(ip) {
			return ErrTargetBlocked
		}
		return nil
	}
	lowered := strings.ToLower(strings.TrimSuffix(host, "."))
	// 这些主机名永远意味着"就是本机"或者"集群内部"。
	for _, blocked := range []string{"localhost", "ip6-localhost", "ip6-loopback"} {
		if lowered == blocked || strings.HasSuffix(lowered, "."+blocked) {
			return ErrTargetBlocked
		}
	}
	for _, suffix := range []string{".internal", ".local", ".localdomain", ".cluster.local"} {
		if strings.HasSuffix(lowered, suffix) {
			return ErrTargetBlocked
		}
	}
	if lowered == "metadata.google.internal" || lowered == "metadata.goog" {
		return ErrTargetBlocked
	}
	// 先解析再放行：一个指向内网地址的公网名字，
	// 正是攻击者会用的那种情况。
	addresses, err := net.LookupIP(host)
	if err != nil {
		// 解析不出来的主机，同样也不可能是能用的端点。
		return ErrTargetBlocked
	}
	for _, ip := range addresses {
		if isPrivateAddress(ip) {
			return ErrTargetBlocked
		}
	}
	return nil
}

func isPrivateAddress(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		// 运营商级 NAT、云元数据（169.254.169.254）以及 0.0.0.0/8。
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
		if v4[0] == 169 && v4[1] == 254 {
			return true
		}
		if v4[0] == 0 {
			return true
		}
		// 共享地址空间（192.0.0.0/24，RFC 6890）。
		if v4[0] == 192 && v4[1] == 0 && v4[2] == 0 {
			return true
		}
		// 198.18.0.0/15 是留给基准测试用的，但好几个开发用的 DNS 解析器
		// 也会拿它来应答每一个公网名字。把它一并拦掉，
		// 就会误伤这些网络上的合法端点，所以这里放行。
		return false
	}
	// IPv6 唯一本地地址（fc00：：/7）。
	return ip[0]&0xfe == 0xfc
}
