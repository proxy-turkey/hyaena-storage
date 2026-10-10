package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/proxy-turkey/hyaena-storage/internal/core"
)

// writeJSON, yanıtı JSON olarak yazar.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr, FastAPI uyumlu hata döndürür: {"detail": msg}.
func writeErr(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}

// urlParam, chi path parametresini döndürür.
func urlParam(r *http.Request, key string) string {
	return chi.URLParam(r, key)
}

// adminTokenCookie, admin çerezini okur.
func (sv *Server) adminTokenCookie(r *http.Request) string {
	c, err := r.Cookie(sv.s.AdminCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// verifyAdmin, isteğin admin token'ını doğrular: MAC (kova+nonce) VE
// sunucu-taraflı oturum kaydı — logout/iptal edilmiş çerez geçersizdir (F-06).
func (sv *Server) verifyAdmin(r *http.Request) bool {
	tok := sv.adminTokenCookie(r)
	if tok == "" {
		return false
	}
	if !core.VerifyAdminToken(sv.s.TokenSecret(), sv.s.TokenTTLHours, tok, time.Now()) {
		return false
	}
	return sv.adminSessionValid(core.AdminTokenSID(tok))
}

// requireAdmin, admin cookie'si olmayan istekleri 401 yapar.
func (sv *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sv.verifyAdmin(r) {
			writeErr(w, http.StatusUnauthorized, "Yetkisiz")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimit, per-IP kayan pencere sınırı uygular (upload/download).
func (sv *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sv.uploadLimiter.Check(sv.clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "Çok fazla istek")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loginLimit, admin login'e sıkı per-IP sınırı + global deneme bütçesi uygular
// (F-02/F-11): IP rotasyonu / CF-aralığı header güveni aşılsa bile toplam
// deneme hacmi sınırlı kalır.
func (sv *Server) loginLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sv.loginLimiter.Check(sv.clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "Çok fazla deneme, birazdan tekrar dene")
			return
		}
		if sv.globalLoginLimiter != nil && !sv.globalLoginLimiter.Check("global") {
			writeErr(w, http.StatusTooManyRequests, "Çok fazla deneme, birazdan tekrar dene")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminChannelGuard, admin API'yi yalnızca Cloudflare üzerinden (TLS) veya
// VPS yerelinden (127.0.0.1 bakımı) erişilebilir kılar (B-06). Doğrudan
// origin'e (direct.hyaena.co.uk:8080, şifresiz HTTP) gelen admin istekleri
// 403 alır — admin cookie'si açık ağda dinlenemesin. Karar TCP seviyesindeki
// eş adresinden verilir; X-Forwarded-For'a güvenilmez (spoof'a kapalı).
func (sv *Server) adminChannelGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sv.s.AdminAllowDirect {
			next.ServeHTTP(w, r)
			return
		}
		peer := net.ParseIP(core.RemoteIP(r))
		if peer == nil {
			writeErr(w, http.StatusForbidden, "Yasak kanal")
			return
		}
		if peer.IsLoopback() {
			next.ServeHTTP(w, r)
			return
		}
		// CF-aralığı peer'leri paylaşımlı egress'tir: herhangi bir CF müşterisi
		// bu aralıklardan origin'e bağlanabilir. Kanal güveni artık Worker↔origin
		// paylaşımlı sırra bağlanır (F-02); sır yoksa fail-closed.
		if core.IsCloudflareIP(peer) {
			if sv.s.OriginSecret == "" {
				writeErr(w, http.StatusServiceUnavailable, "Origin secret yapılandırılmamış (ORIGIN_SECRET)")
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Origin-Secret")), []byte(sv.s.OriginSecret)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		writeErr(w, http.StatusForbidden, "Admin paneli yalnızca https://storage.hyaena.co.uk üzerinden erişilebilir")
	})
}

// clientIP, hız-limit anahtarını üretir (F-02/F-11): ileri başlıklar yalnızca
// geçerli origin sırrı taşıyan CF-eşlerinden kabul edilir — CF-aralığındaki
// herhangi bir saldırgan sahte CF-Connecting-IP ile limit anahtarını seçemez.
// Değer IPv6'da /64 ile normalleştirilir (adres döndürme tek kova üretir).
func (sv *Server) clientIP(r *http.Request) string {
	peer := core.RemoteIP(r)
	peerIP := net.ParseIP(peer)
	if sv.s.OriginSecret != "" && peerIP != nil && core.IsCloudflareIP(peerIP) &&
		subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Origin-Secret")), []byte(sv.s.OriginSecret)) == 1 {
		return core.ClientIP(r)
	}
	return core.NormalizeIPKey(peer)
}

// mimeFileServer, embed FS'ten MIME türünü doğru ayarlayarak dosya servis eder.
// http.FileServer fs.FS'de MIME map'ini kuramaz; uzantıya göre Content-Type set edilir.
// Statik dosyalar no-cache servis edilir: UI güncellemeleri (app.js/html/css)
// Cloudflare edge cache'inde 31 gün takılı kalmasın. Download'ların uzun TTL'si
// ayrı endpoint olduğu için etkilenmez.
func mimeFileServer(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if ct := mimeTypeByExt(name); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.FileServer(http.FS(fsys)).ServeHTTP(w, r)
	})
}

// mimeTypeByExt, bilinen uzantılar için Content-Type döndürür.
func mimeTypeByExt(name string) string {
	switch {
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".jpg"), strings.HasSuffix(name, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	default:
		return ""
	}
}
