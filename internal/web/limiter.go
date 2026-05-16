package web

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type limiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     float64
	burst    float64
	maxKeys  int
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(rate, burst float64) *limiter {
	return &limiter{
		buckets: make(map[string]*bucket),
		rate:    rate,
		burst:   burst,
		maxKeys: 10000,
	}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.maxKeys {
			l.evictLocked()
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *limiter) evictLocked() {
	cutoff := time.Now().Add(-30 * time.Minute)
	for k, b := range l.buckets {
		if b.last.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
	if len(l.buckets) < l.maxKeys {
		return
	}
	drop := len(l.buckets) / 2
	for k := range l.buckets {
		if drop == 0 {
			break
		}
		delete(l.buckets, k)
		drop--
	}
}

// clientIP extracts the caller's IP. When sinau is behind a trusted reverse
// proxy that sets X-Real-IP or X-Forwarded-For (see DEPLOYMENT.md nginx
// example), those headers are honoured. When exposed directly, RemoteAddr is
// used. Trusting forwarded headers is only safe when the proxy is the sole
// path to the app (e.g. binding to 127.0.0.1).
func clientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
