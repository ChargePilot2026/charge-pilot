// Package netguard rejects outbound targets that would let a caller reach
// infrastructure the platform must not expose, such as the loopback interface
// or a cloud metadata endpoint. It lives in platform because both central and
// the worker must apply the identical rule.
package netguard

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// ErrTargetBlocked marks a subscription endpoint that would let an
// operator reach infrastructure the platform should not expose. It is enforced
// both when a subscription is created and again before every delivery, because
// a hostname can start resolving to a private address later.
var ErrTargetBlocked = errors.New("订阅地址指向非公网目标，已拒绝")

// ValidatePublicHTTPS rejects anything that is not a public HTTPS endpoint.
// Blocking at creation stops an internal target from ever entering the table;
// re-checking at delivery closes the DNS-rebinding case.
func ValidatePublicHTTPS(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ErrTargetBlocked
	}
	// Userinfo in a URL is a classic credential-leak pattern and has no place
	// in a webhook target.
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
	// Hostnames that always mean "this machine" or "inside the cluster".
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
	// Resolve before accepting: a public name that points at a private address is
	// exactly the case an attacker would use.
	addresses, err := net.LookupIP(host)
	if err != nil {
		// An unresolvable host cannot be a working endpoint either.
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
		// Carrier-grade NAT, cloud metadata (169.254.169.254) and 0.0.0.0/8.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
		if v4[0] == 169 && v4[1] == 254 {
			return true
		}
		if v4[0] == 0 {
			return true
		}
		// Shared address space (192.0.0.0/24, RFC 6890).
		if v4[0] == 192 && v4[1] == 0 && v4[2] == 0 {
			return true
		}
		// 198.18.0.0/15 is reserved for benchmarking, but is also what several
		// development DNS resolvers hand out for every public name. Blocking it
		// would reject legitimate endpoints on those networks, so it is allowed.
		return false
	}
	// Unique local IPv6 (fc00::/7).
	return ip[0]&0xfe == 0xfc
}
