package httpapi

import (
	"net"
	"net/http"
	"strings"
)

// proxyRealIP replaces r.RemoteAddr with the client IP that the reverse proxy
// put in X-Real-IP. Our nginx sets that header to $remote_addr, overwriting
// whatever the client sent, so it is the one header a client cannot fake.
//
// chi's middleware.RealIP is not used because it reads True-Client-IP first,
// which nginx passes through untouched.
//
// The header is only believed when the request comes from a loopback or
// private address, i.e. from nginx on the same host or the Docker network. A
// request that reaches the API port straight from the internet keeps its real
// peer address.
func proxyRealIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fromTrustedProxy(r.RemoteAddr) {
			if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
				r.RemoteAddr = ip.String()
			}
		}
		next.ServeHTTP(w, r)
	})
}

func fromTrustedProxy(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}
