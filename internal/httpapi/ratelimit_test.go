package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIPRateLimiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1 per minute with a burst of 3: the 4th request in a row is rejected.
	limiter := newIPRateLimiter(ctx, 1, 3)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	call := func(remoteAddr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/files", nil)
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	for i := 0; i < 3; i++ {
		if rec := call("203.0.113.7:5000"); rec.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i+1, rec.Code)
		}
	}

	rec := call("203.0.113.7:5001")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over limit: got %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 response has no Retry-After header")
	}

	if rec := call("198.51.100.1:5000"); rec.Code != http.StatusOK {
		t.Fatalf("other IP: got %d, want 200", rec.Code)
	}
}

func TestIPRateLimiterDisabled(t *testing.T) {
	if l := newIPRateLimiter(context.Background(), 0, 10); l != nil {
		t.Fatal("perMinute 0 should disable the limiter")
	}
	var l *ipRateLimiter
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if l.Middleware(next) == nil {
		t.Fatal("nil limiter should pass requests through")
	}
}

func TestClientKey(t *testing.T) {
	tests := []struct {
		remoteAddr string
		want       string
	}{
		{"203.0.113.7:5000", "203.0.113.7"},
		{"203.0.113.7", "203.0.113.7"}, // as set by middleware.RealIP
		{"[2001:db8:1:2:aaaa::1]:443", "2001:db8:1:2::/64"},
		{"[2001:db8:1:2:bbbb::9]:443", "2001:db8:1:2::/64"},
		{"2001:db8:1:3::1", "2001:db8:1:3::/64"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tt.remoteAddr
		if got := clientKey(req); got != tt.want {
			t.Errorf("clientKey(%q) = %q, want %q", tt.remoteAddr, got, tt.want)
		}
	}
}
