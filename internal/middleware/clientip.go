package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// DefaultTrustedProxies covers loopback and private/Docker networks — where a
// local reverse proxy (Nginx Proxy Manager here) connects from.
const DefaultTrustedProxies = "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"

// ClientIP replaces chi's deprecated middleware.RealIP, which trusted
// client-supplied True-Client-IP / X-Real-IP / X-Forwarded-For from anyone
// (GHSA-3fxj-6jh8-hvhx et al.) and so let a caller dodge the per-IP rate
// limits by forging a header.
//
// The header is honored only when the TCP peer is a trusted proxy. The app's
// port is also reachable directly, so a header from any other peer is
// ignored and RemoteAddr stays the real peer address. From a trusted proxy,
// X-Real-IP is used (nginx overwrites it with $remote_addr on every request),
// falling back to the rightmost X-Forwarded-For entry (the one the proxy
// appended). r.RemoteAddr is rewritten so httprate.LimitByIP and the request
// logger see the client.
func ClientIP(trustedCIDRs string) func(http.Handler) http.Handler {
	var trusted []netip.Prefix
	for _, c := range strings.Split(trustedCIDRs, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		p, err := netip.ParsePrefix(c)
		if err != nil {
			slog.Warn("ignoring invalid trusted proxy CIDR", "cidr", c, "err", err)
			continue
		}
		trusted = append(trusted, p.Masked())
	}
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer, ok := parseAddr(r.RemoteAddr)
			if ok && isTrusted(peer) {
				if ip, ok := headerIP(r); ok {
					r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// headerIP extracts the client address set by the trusted proxy.
func headerIP(r *http.Request) (netip.Addr, bool) {
	if v := r.Header.Values("X-Real-IP"); len(v) > 0 {
		// Last value wins: it was set by the hop closest to us.
		if a, err := netip.ParseAddr(strings.TrimSpace(v[len(v)-1])); err == nil {
			return a.Unmap(), true
		}
		return netip.Addr{}, false // fail closed on garbage
	}
	xff := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if xff == "" {
		return netip.Addr{}, false
	}
	parts := strings.Split(xff, ",")
	a, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1]))
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

func parseAddr(remote string) (netip.Addr, bool) {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap().WithZone(""), true
}
