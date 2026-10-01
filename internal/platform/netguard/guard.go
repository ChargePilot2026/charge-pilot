// Package netguard 校验出站目标，阻止访问回环、云元数据等内部基础设施。
// central 与 worker 共用相同的目标限制。
package netguard

import (
	"errors"
	"net"
	"net/url"
	"slices"
	"strings"
)

// ErrTargetBlocked 标记一个会让运维触达平台不应暴露的基础设施的订阅端点。
// 它在创建订阅时拦一道，每次投递前再拦一道，
// 因为一个主机名可能稍后才开始解析到内网地址。
var ErrTargetBlocked = errors.New("订阅地址指向非公网目标，已拒绝")

// ValidatePublicHTTPS 校验目标使用 HTTPS 且不指向本机、内网或云元数据地址。
// 创建和投递时均需调用，投递前重新解析 DNS 以降低重绑定风险。
func ValidatePublicHTTPS(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ErrTargetBlocked
	}
	// 拒绝非 HTTPS、缺少主机或携带 userinfo 凭据的目标地址。
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
	// 拒绝代表本机或集群内部的主机名。
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
	// 解析主机名后检查全部地址，拒绝域名指向内网的请求。
	addresses, err := net.LookupIP(host)
	if err != nil {
		// 解析不出来的主机，同样也不可能是能用的端点。
		return ErrTargetBlocked
	}
	if slices.ContainsFunc(addresses, isPrivateAddress) {
		return ErrTargetBlocked
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
		// 基准测试网段（198.18.0.0/15，RFC 2544）。原仅为兼容开发环境 DNS 映射的放行已按 D3 决策关闭；
		// 若开发环境仍需要该映射，应以配置项方式仅对 dev profile 放行。
		if v4[0] == 198 && v4[1] >= 18 && v4[1] <= 19 {
			return true
		}
		return false
	}
	// IPv6 唯一本地地址（fc00：：/7）。
	return ip[0]&0xfe == 0xfc
}
