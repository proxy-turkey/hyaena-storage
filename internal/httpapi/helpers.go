package httpapi

import (
	"encoding/json"
	"io/fs"
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

// verifyAdmin, isteğin admin token'ını doğrular.
func (sv *Server) verifyAdmin(r *http.Request) bool {
	tok := sv.adminTokenCookie(r)
	if tok == "" {
		return false
	}
	return core.VerifyAdminToken(sv.s.TokenSecret(), sv.s.TokenTTLHours, tok, time.Now())
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
		if !sv.uploadLimiter.Check(core.ClientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "Çok fazla istek")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loginLimit, admin login'e sıkı per-IP sınırı uygular (brute-force koruması).
func (sv *Server) loginLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sv.loginLimiter.Check(core.ClientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "Çok fazla deneme, birazdan tekrar dene")
			return
		}
		next.ServeHTTP(w, r)
	})
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
