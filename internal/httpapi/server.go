// Package httpapi, tüm HTTP yüzeyini sunar (Python app/api.py + app/main.py karşılığı).
package httpapi

import (
	"context"
	"io/fs"
	"net/http"

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

	adminLimiter *core.RateLimiter
	uploadLimiter *core.RateLimiter
	loginLimiter *core.RateLimiter
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

	r.Get("/health", sv.health)
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
