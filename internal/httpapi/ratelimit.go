package httpapi

import (
	"context"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	limiterIdleTTL      = 10 * time.Minute
	limiterSweepEvery   = time.Minute
	ipv6PrefixBits      = 64
	rateLimitRetryFloor = time.Second
)

// ipRateLimiter is a token bucket per client IP, kept in memory. Each API
// instance counts on its own, so with N replicas a client gets N times the
// limit; move the counters to Redis if that ever matters.
type ipRateLimiter struct {
	limit rate.Limit
	burst int

	mu      sync.Mutex
	clients map[string]*clientLimiter
}

type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// newIPRateLimiter allows perMinute requests per minute on average, with bursts
// of up to burst requests. perMinute <= 0 disables the limiter.
func newIPRateLimiter(ctx context.Context, perMinute, burst int) *ipRateLimiter {
	if perMinute <= 0 {
		return nil
	}
	if burst < 1 {
		burst = 1
	}
	l := &ipRateLimiter{
		limit:   rate.Limit(float64(perMinute) / 60),
		burst:   burst,
		clients: make(map[string]*clientLimiter),
	}
	go l.sweep(ctx)
	return l
}

func (l *ipRateLimiter) get(key string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	c, ok := l.clients[key]
	if !ok {
		c = &clientLimiter{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.clients[key] = c
	}
	c.lastSeen = time.Now()
	return c.limiter
}

// sweep forgets clients that have been quiet long enough for their bucket to
// refill, so the map does not grow with every IP ever seen.
func (l *ipRateLimiter) sweep(ctx context.Context) {
	ticker := time.NewTicker(limiterSweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-limiterIdleTTL)
			l.mu.Lock()
			for key, c := range l.clients {
				if c.lastSeen.Before(cutoff) {
					delete(l.clients, key)
				}
			}
			l.mu.Unlock()
		}
	}
}

// Middleware rejects requests over the limit with 429 and a Retry-After header.
// A nil limiter passes everything through.
func (l *ipRateLimiter) Middleware(next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res := l.get(clientKey(r)).Reserve()
		if delay := res.Delay(); delay > 0 {
			// Give the token back: a rejected request should not push the
			// client's next allowed request further out.
			res.Cancel()
			if delay < rateLimitRetryFloor {
				delay = rateLimitRetryFloor
			}
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(delay.Seconds()))))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests. Please wait a moment and try again.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientKey identifies the caller by IP. IPv6 clients usually control a whole
// /64, so they are grouped by that prefix; otherwise rotating addresses inside
// it would get around the limit.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr // middleware.RealIP stores a bare IP without a port
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if ip.To4() != nil {
		return ip.String()
	}
	return ip.Mask(net.CIDRMask(ipv6PrefixBits, 128)).String() + "/64"
}
