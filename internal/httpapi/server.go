// Package httpapi, tüm HTTP yüzeyini sunar (Python app/api.py + app/main.py karşılığı).
package httpapi

import (
	"context"
	"io/fs"
	"net/http"
	"net/url"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/proxy-turkey/hyaena-storage/internal/config"
	"github.com/proxy-turkey/hyaena-storage/internal/core"
	"github.com/proxy-turkey/hyaena-storage/internal/storage"
	"github.com/proxy-turkey/hyaena-storage/internal/tgworker"
)

// Server, HTTP handler ve paylaşılan durumu tutar.
type Server struct {
	s     *config.Settings
	store *storage.Store
	tw    tgworker.Worker
	static fs.FS
	ctx   context.Context // uygulama-ömürlü context (arka plan upload'ları için)

	fleetOnce sync.Once // EnsureFleet arka plan çağrısını tek seferlik yapar (goroutine sızıntısı önler)

	adminLimiter *core.RateLimiter
	uploadLimiter *core.RateLimiter
	loginLimiter *core.RateLimiter
}

// securityHeaders, temel güvenlik başlıklarını tüm yanıtlara ekler.
// CSP eklenmez: frontend inline script kullanıyor.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// adminOriginGuard, admin mutating isteklerinde CSRF koruması: Origin/Referer
// header'ı varsa isteğin host'uyla eşleşmeli. SameSite=Lax zaten form-CSRF'yi
// büyük ölçüde engeller; bu ek katmandır. Origin yoksa (curl, aynı-origin) geçer.
func adminOriginGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		ref := r.Header.Get("Referer")
		host := r.Host
		if origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != host {
				writeErr(w, http.StatusForbidden, "Geçersiz kaynak")
				return
			}
		} else if ref != "" {
			u, err := url.Parse(ref)
			if err != nil || u.Host != host {
				writeErr(w, http.StatusForbidden, "Geçersiz kaynak")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ensureFleet, kanal filosunu arka planda en fazla bir kez garanti eder.
// Her upload'ta goroutine fırlatmak yerine (goroutine sızıntısı + Telegram
// flood riski), ilk çağrıda bir kere çalışır.
func (sv *Server) ensureFleet() {
	if sv.tw == nil {
		return
	}
	sv.fleetOnce.Do(func() {
		go func() { _ = sv.tw.EnsureFleet(sv.ctx) }()
	})
}

// New, chi router'ını kurar. static: //go:embed static (fs.FS).
// ctx: uygulama genelinde yaşayan context (arka plan upload görevleri için).
func New(ctx context.Context, s *config.Settings, store *storage.Store, tw tgworker.Worker, static fs.FS) http.Handler {
	sv := &Server{
		s:            s,
		store:        store,
		tw:           tw,
		static:       static,
		ctx:          ctx,
		adminLimiter: core.NewRateLimiter(s.RateLimitPerMin, 60),
		uploadLimiter: core.NewRateLimiter(s.RateLimitPerMin, 60),
		loginLimiter: core.NewRateLimiter(5, 60), // 5 login denemesi / dk
	}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	r.Use(securityHeaders)

	r.Get("/health", sv.health)
	r.Get("/bandwidth", sv.bandwidthPage)
	r.Get("/", sv.index)
	r.Get("/admin", sv.adminPage)

	// statik dosyalar (embed) — /static/* → staticFS içindeki static/ alt dizini
	sub, err := fs.Sub(static, "static")
	if err == nil {
		r.Handle("/static/*", http.StripPrefix("/static/", mimeFileServer(sub)))
	}

	// eski /download/... linkleri → /api/download/... (geriye dönük uyumluluk)
	r.Get("/download/{token}", sv.legacyDownload)
	r.Get("/download/{token}/{name}", sv.legacyDownload)

	r.Route("/api", func(api chi.Router) {
		// açık upload (rate-limited)
		api.With(sv.rateLimit).Post("/upload/start", sv.uploadStart)
		api.With(sv.rateLimit).Post("/upload/by-url", sv.uploadByURL)
		// parts: token doğrulanmış + parça boyutu sınırlı; büyük dosyalar için rate-limit'siz
		api.Post("/upload/{token}/parts/{index}", sv.uploadPart)
		api.With(sv.rateLimit).Post("/upload/{token}/finish", sv.uploadFinish)
		api.Get("/upload/{token}/status", sv.uploadStatus)
		api.Delete("/upload/{token}", sv.uploadCancel)

		// açık download (rate-limited)
		api.With(sv.rateLimit).Get("/download/{token}", sv.downloadMeta)
		api.With(sv.rateLimit).Get("/download/{token}/{name}", sv.downloadFile)

		// admin auth (public) — login'e brute-force koruması için sıkı rate limit
		api.With(sv.loginLimit).Post("/admin/login", sv.adminLogin)
		api.Post("/admin/logout", sv.adminLogout)
		api.Get("/admin/session", sv.adminSession)

		// admin korumalı
		api.Group(func(a chi.Router) {
			a.Use(sv.requireAdmin)
			a.Use(adminOriginGuard)
			a.Get("/admin/summary", sv.adminSummary)
			a.Get("/admin/channels", sv.adminChannels)
			a.Post("/admin/channels/create-now", sv.adminCreateChannel)
			a.Get("/admin/files", sv.adminFiles)
			a.Delete("/admin/files/{token}", sv.adminDeleteFile)
			a.Post("/admin/files/{token}/resync", sv.adminResync)
			a.Get("/admin/settings", sv.adminSettings)
		})
	})

	return r
}
