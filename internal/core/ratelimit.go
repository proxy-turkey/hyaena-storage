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
	lastPrune     time.Time
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
// Anahtarlar periyodik budanır: aylarca çalışan serviste hits haritası
// sınırsız büyümesin (pencere dışına düşen IP'lerin kayıtları silinir).
func (r *RateLimiter) Check(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	window := time.Duration(r.windowSeconds * float64(time.Second))
	if now.Sub(r.lastPrune) > window {
		r.pruneLocked(now.Add(-window))
		r.lastPrune = now
	}
	cutoff := now.Add(-window)
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

// pruneLocked, cutoff'tan önceki tüm kayıtları düşmüş anahtarları haritadan siler.
func (r *RateLimiter) pruneLocked(cutoff time.Time) {
	for k, hits := range r.hits {
		alive := false
		for _, t := range hits {
			if t.After(cutoff) {
				alive = true
				break
			}
		}
		if !alive {
			delete(r.hits, k)
		}
	}
}

// cloudflareCIDRs, Cloudflare'nin yayınlanan uç IP aralıkları
// (https://www.cloudflare.com/ips/). Origin'e gelen bağlantının gerçek istemci
// mi yoksa CF proxy mi olduğunu ayırt etmek için kullanılır.
var cloudflareCIDRs = mustParseCIDRs([]string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
})

func mustParseCIDRs(specs []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(specs))
	for _, s := range specs {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			panic("geçersiz CIDR: " + s)
		}
		out = append(out, n)
	}
	return out
}

// IsCloudflareIP, IP'nin Cloudflare uç aralıklarında olup olmadığını döndürür.
// (httpapi'nin direct-origin admin guard'ı da kullanır — bkz. B-06.)
func IsCloudflareIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range cloudflareCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// RemoteIP, TCP seviyesindeki gerçek eş adresini döndürür (RemoteAddr).
func RemoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ClientIP, istemcinin gerçek IP'sini döndürür.
//
// İleri başlıkları (CF-Connecting-IP / X-Forwarded-For) YALNIZCA bağlantı
// Cloudflare'den geliyorsa kabul eder: CF bu başlıkları üstüne yazar, proxy
// üzerinden gelince sahtelenemez. Doğrudan origin'e (direct.hyaena.co.uk:8080)
// bağlanan istemciler için başlıklar yok sayılır ve RemoteAddr esas alınır —
// aksi halde XFF sahtelemekle per-IP rate limit atlanabilirdi.
// Anahtar IPv6'da /64 ile normalleştirilir: adres döndürme limit geçersiz kılar.
func ClientIP(r *http.Request) string {
	peer := RemoteIP(r)
	client := peer
	if IsCloudflareIP(net.ParseIP(peer)) {
		if cip := r.Header.Get("CF-Connecting-IP"); cip != "" {
			client = cip
		} else if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			client = strings.TrimSpace(strings.Split(fwd, ",")[0])
		}
	}
	return NormalizeIPKey(client)
}

// NormalizeIPKey, hız-limit anahtarı için IP'yi normalleştirir (F-11):
// IPv4 kanonik ondalık; IPv6 /64 maskelenir (tek tahsis = tek kova);
// ayrıştırılamayan değer ham döner.
func NormalizeIPKey(s string) string {
	ip := net.ParseIP(s)
	if ip == nil {
		return s
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}
