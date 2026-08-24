package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBucketLimiterEnforcesBurst(t *testing.T) {
	t.Parallel()
	// 1/s with a burst of 3: the first three are free, the fourth is not.
	l := newBucketLimiter(1, 3)
	defer close(l.stop)

	for i := 0; i < 3; i++ {
		if !l.allow("tenant-a") {
			t.Fatalf("request %d denied inside the burst", i+1)
		}
	}
	if l.allow("tenant-a") {
		t.Error("fourth request allowed; the burst was not enforced")
	}
}

func TestBucketLimiterIsolatesKeys(t *testing.T) {
	t.Parallel()
	// One noisy tenant must not consume another tenant's allowance.
	l := newBucketLimiter(1, 2)
	defer close(l.stop)

	for i := 0; i < 2; i++ {
		l.allow("noisy")
	}
	if l.allow("noisy") {
		t.Fatal("noisy tenant was not limited")
	}
	if !l.allow("quiet") {
		t.Error("quiet tenant was limited by another tenant's traffic")
	}
}

func TestBucketLimiterEvictsIdleBuckets(t *testing.T) {
	t.Parallel()
	l := newBucketLimiter(1, 1)
	defer close(l.stop)

	l.allow("transient")
	l.mu.Lock()
	if len(l.buckets) != 1 {
		l.mu.Unlock()
		t.Fatalf("expected 1 bucket, got %d", len(l.buckets))
	}
	// Backdate the sighting so the sweep considers it idle.
	l.lastSeen["transient"] = time.Now().Add(-2 * l.idleTTL)
	l.mu.Unlock()

	// Drive one sweep directly rather than waiting on the ticker.
	l.idleTTL = time.Nanosecond
	go l.evictLoop(time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		n := len(l.buckets)
		l.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("idle bucket was never evicted; the map would grow without bound")
}

func TestRateLimitPerIPReturns429WithRetryAfter(t *testing.T) {
	t.Parallel()
	handler := RateLimitPerIP(1, 1)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
		r.RemoteAddr = "203.0.113.7:44444"
		return r
	}

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, newReq())
	if first.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", first.Code)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, newReq())
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", second.Code)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Error("429 response is missing Retry-After")
	}
}

func TestRateLimitDisabledWhenRateNonPositive(t *testing.T) {
	t.Parallel()
	// Zero must be a clean off switch, not a limiter that denies everything.
	handler := RateLimitPerIP(0, 0)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for i := 0; i < 50; i++ {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "203.0.113.7:44444"
		handler.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200 with the limiter disabled", i, rec.Code)
		}
	}
}

func TestClientIPHandlesBareAddress(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// RealIP can leave a bare IP with no port.
	r.RemoteAddr = "198.51.100.9"
	if got := clientIP(r); got != "198.51.100.9" {
		t.Errorf("clientIP = %q, want the bare address back", got)
	}
}
