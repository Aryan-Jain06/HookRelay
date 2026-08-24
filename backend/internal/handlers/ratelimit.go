package handlers

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/aryan-jain06/hookrelay/backend/internal/httpx"
	"github.com/aryan-jain06/hookrelay/backend/internal/metrics"
	"golang.org/x/time/rate"
)

// bucketLimiter holds one token bucket per key, evicting buckets that have gone
// quiet so the map cannot grow without bound.
//
// State is per process, so with N API replicas the effective ceiling is N times
// the configured rate. That is an accepted approximation: the goal is to stop
// one runaway client from exhausting the database, not to meter usage exactly.
// An exact global limit needs a shared counter in Redis.
type bucketLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*rate.Limiter
	lastSeen map[string]time.Time
	perSec   rate.Limit
	burst    int

	idleTTL time.Duration
	stop    chan struct{}
}

func newBucketLimiter(perSec float64, burst int) *bucketLimiter {
	l := &bucketLimiter{
		buckets:  make(map[string]*rate.Limiter),
		lastSeen: make(map[string]time.Time),
		perSec:   rate.Limit(perSec),
		burst:    burst,
		idleTTL:  30 * time.Minute,
		stop:     make(chan struct{}),
	}
	go l.evictLoop(10 * time.Minute)
	return l
}

// allow consumes a token for key.
func (l *bucketLimiter) allow(key string) bool {
	l.mu.Lock()
	lim, ok := l.buckets[key]
	if !ok {
		lim = rate.NewLimiter(l.perSec, l.burst)
		l.buckets[key] = lim
	}
	l.lastSeen[key] = time.Now()
	l.mu.Unlock()
	return lim.Allow()
}

func (l *bucketLimiter) evictLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			cutoff := time.Now().Add(-l.idleTTL)
			l.mu.Lock()
			for k, seen := range l.lastSeen {
				if seen.Before(cutoff) {
					delete(l.buckets, k)
					delete(l.lastSeen, k)
				}
			}
			l.mu.Unlock()
		}
	}
}

// RateLimitPerTenant throttles authenticated requests per tenant. Mount it
// inside the authenticated group, after the auth middleware, so a tenant is
// already resolved. A non-positive rate disables it.
func RateLimitPerTenant(perSec float64, burst int) func(http.Handler) http.Handler {
	if perSec <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	limiter := newBucketLimiter(perSec, burst)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenant := TenantFrom(r.Context())
			if tenant == nil {
				// Unauthenticated requests are the IP limiter's problem.
				next.ServeHTTP(w, r)
				return
			}
			if !limiter.allow(tenant.ID.String()) {
				metrics.RateLimited.WithLabelValues("tenant").Inc()
				tooManyRequests(w, r, "too many requests for this tenant")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimitPerIP throttles by client address. It guards the unauthenticated
// endpoints, where there is no tenant yet and bcrypt makes each attempt
// expensive. A non-positive rate disables it.
func RateLimitPerIP(perSec float64, burst int) func(http.Handler) http.Handler {
	if perSec <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	limiter := newBucketLimiter(perSec, burst)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.allow(clientIP(r)) {
				metrics.RateLimited.WithLabelValues("ip").Inc()
				tooManyRequests(w, r, "too many requests from this address")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func tooManyRequests(w http.ResponseWriter, r *http.Request, msg string) {
	w.Header().Set("Retry-After", strconv.Itoa(1))
	httpx.Error(w, r, httpx.Errorf(http.StatusTooManyRequests, "rate_limited", msg))
}

// clientIP returns the address to key an IP limit on.
//
// chi's RealIP middleware has already rewritten RemoteAddr from X-Forwarded-For
// or X-Real-IP where present, which is correct behind the TLS proxy this is
// designed to run behind. Note the consequence: exposed directly to the
// internet, a client could spoof those headers and sidestep this limit, so the
// proxy that overwrites them is part of the protection.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr is not always host:port (RealIP leaves a bare IP).
		return r.RemoteAddr
	}
	return host
}
