package core

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RateLimiter, kayan pencereli per-IP istek sınırlayıcıdır (Python birebir).
type RateLimiter struct {
	mu            sync.Mutex
	limit         int
	windowSeconds float64
	hits          map[string][]time.Time
}

// NewRateLimiter, limit adet isteği windowSeconds penceresinde kabul eden sınırlayıcı.
func NewRateLimiter(limit int, windowSeconds float64) *RateLimiter {
	return &RateLimiter{
		limit:         limit,
		windowSeconds: windowSeconds,
		hits:          map[string][]time.Time{},
	}
}

// Check, key için pencere içinde istek izni var mı döndürür; varsa kaydı ekler.
func (r *RateLimiter) Check(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-time.Duration(r.windowSeconds * float64(time.Second)))
	hits := r.hits[key]
	kept := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= r.limit {
		r.hits[key] = kept
		return false
	}
	r.hits[key] = append(kept, now)
	return true
}

// ClientIP, proxy varsa X-Forwarded-For ilk değeri, yoksa RemoteAddr.
func ClientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
