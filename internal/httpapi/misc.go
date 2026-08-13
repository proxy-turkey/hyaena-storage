package httpapi

import (
	"context"
	"io/fs"
	"net/http"
	"time"
)

// appConfig, frontend'in ihtiyaç duyduğu yapılandırmayı döndürür.
//
// İki taban URL vardır:
//   - public_base_url: Cloudflare cache'li (storage.hyaena.co.uk) — küçük
//     dosyalar buradan servis edilir (egress bypass).
//   - direct_base_url: Cloudflare'sız (direct.hyaena.co.uk:8080) — büyük
//     dosyalar buradan (Cloudflare büyük stream'leri kestiği için).
//
// Frontend linki dosya boyutuna göre seçer (bkz. sizeThresholdBytes).
func (sv *Server) appConfig(w http.ResponseWriter, r *http.Request) {
	pub := sv.s.PublicBaseURL
	if pub == "" {
		pub = "https://" + r.Host
	}
	dir := sv.s.DirectBaseURL
	if dir == "" {
		dir = pub
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"public_base_url":    pub,
		"direct_base_url":    dir,
		"size_threshold":     sv.s.DirectThresholdBytes,
	})
}

func (sv *Server) health(w http.ResponseWriter, r *http.Request) {
	ready := false
	if sv.tw != nil {
		select {
		case <-sv.tw.Ready():
			ready = true
		case <-time.After(5 * time.Millisecond):
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "telegram_ready": ready})
}

func (sv *Server) index(w http.ResponseWriter, r *http.Request) {
	serveFile(w, r, sv.static, "static/index.html")
}

func (sv *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	serveFile(w, r, sv.static, "static/admin.html")
}

// legacyDownload, eski /download/... linklerini /api/download/...'a yönlendirir.
func (sv *Server) legacyDownload(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/api"+r.URL.Path, http.StatusTemporaryRedirect)
}

func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, path string) {
	// embed FS'ten dosyayı serve et (MIME embed'de otomatik kurulamaz)
	if ct := mimeTypeByExt(path); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// HTML sayfaları no-cache: güncellemeler (marka, metinler) anında yayılsın.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, fsys, path)
}

// waitWorker, Telegram worker'ın hazır olmasını bekler; hazır değilse 503.
func (sv *Server) waitWorker(w http.ResponseWriter, r *http.Request) bool {
	if sv.tw == nil {
		writeErr(w, http.StatusServiceUnavailable, "Telegram worker yok")
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := sv.tw.WaitReady(ctx); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "Telegram hazır değil (login bekleniyor)")
		return false
	}
	return true
}
