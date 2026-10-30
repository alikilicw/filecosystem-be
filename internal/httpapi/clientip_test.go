package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyRealIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{
			name:       "nginx on the host",
			remoteAddr: "127.0.0.1:41000",
			headers:    map[string]string{"X-Real-IP": "203.0.113.7"},
			want:       "203.0.113.7",
		},
		{
			name:       "nginx via the Docker bridge",
			remoteAddr: "172.17.0.1:41000",
			headers:    map[string]string{"X-Real-IP": "2001:db8::1"},
			want:       "2001:db8::1",
		},
		{
			name:       "client-sent True-Client-IP is ignored",
			remoteAddr: "127.0.0.1:41000",
			headers:    map[string]string{"X-Real-IP": "203.0.113.7", "True-Client-IP": "1.2.3.4"},
			want:       "203.0.113.7",
		},
		{
			name:       "client-sent X-Forwarded-For is ignored",
			remoteAddr: "127.0.0.1:41000",
			headers:    map[string]string{"X-Real-IP": "203.0.113.7", "X-Forwarded-For": "1.2.3.4, 203.0.113.7"},
			want:       "203.0.113.7",
		},
		{
			name:       "direct request from the internet keeps its peer",
			remoteAddr: "198.51.100.9:5000",
			headers:    map[string]string{"X-Real-IP": "1.2.3.4"},
			want:       "198.51.100.9:5000",
		},
		{
			name:       "garbage header keeps the peer",
			remoteAddr: "127.0.0.1:41000",
			headers:    map[string]string{"X-Real-IP": "not-an-ip"},
			want:       "127.0.0.1:41000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			h := proxyRealIP(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = r.RemoteAddr
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			if got != tt.want {
				t.Errorf("RemoteAddr = %q, want %q", got, tt.want)
			}
		})
	}
}
