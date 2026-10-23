package middleware

import (
	"context"
	"net"
	"net/http"
	"strings"
)

type clientIPKey struct{}

// ClientIP resolves the client address once per request and stores it in the
// context. The direct peer address is used, without its port, unless the peer
// is the trusted proxy (an IP address or CIDR network); then the right-most
// X-Forwarded-For entry that is not itself a trusted proxy is used, so clients
// cannot spoof the address by prepending their own entries.
func ClientIP(trustedProxy string) Middleware {
	trusted := parseTrustedProxy(trustedProxy)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			ctx := context.WithValue(request.Context(), clientIPKey{}, resolveClientIP(request, trusted))
			next.ServeHTTP(writer, request.WithContext(ctx))
		})
	}
}

// ClientIPFromContext returns the address resolved by ClientIP.
func ClientIPFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	ip, ok := ctx.Value(clientIPKey{}).(string)
	return ip, ok && ip != ""
}

// PeerIP returns the direct peer address without its port.
func PeerIP(remoteAddr string) string {
	if ip := parseAddress(remoteAddr); ip != nil {
		return ip.String()
	}
	return strings.TrimSpace(remoteAddr)
}

func resolveClientIP(request *http.Request, trusted func(net.IP) bool) string {
	peer := parseAddress(request.RemoteAddr)
	if peer == nil {
		return PeerIP(request.RemoteAddr)
	}
	if !trusted(peer) {
		return peer.String()
	}
	hops := strings.Split(strings.Join(request.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop := parseAddress(hops[i])
		if hop == nil {
			break
		}
		if !trusted(hop) {
			return hop.String()
		}
	}
	return peer.String()
}

func parseTrustedProxy(value string) func(net.IP) bool {
	value = strings.TrimSpace(value)
	if _, network, err := net.ParseCIDR(value); err == nil {
		return network.Contains
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.Equal
	}
	return func(net.IP) bool { return false }
}

func parseAddress(value string) net.IP {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	return net.ParseIP(strings.Trim(value, "[]"))
}
