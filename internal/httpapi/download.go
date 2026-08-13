package httpapi

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/proxy-turkey/hyaena-storage/internal/storage"
)

// fileExpired, dosyanın süresi dolmuş mu döndürür (expires_at <= now UTC).
func fileExpired(f *storage.File) bool {
	if f.ExpiresAt == nil || *f.ExpiresAt == "" {
		return false // süresiz
	}
	t, err := time.Parse("2006-01-02 15:04:05", *f.ExpiresAt)
	if err != nil {
		return false
	}
	return time.Now().UTC().After(t)
}

func (sv *Server) downloadMeta(w http.ResponseWriter, r *http.Request) {
	token := urlParam(r, "token")
	if !validToken(token) {
		writeErr(w, http.StatusBadRequest, "Geçersiz token")
		return
	}
	f, err := sv.store.GetFileByToken(token)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Sorgu hatası")
		return
	}
	if f == nil || f.Status != "ready" {
		writeErr(w, http.StatusNotFound, "Dosya bulunamadı veya hazır değil")
		return
	}
	if fileExpired(f) {
		writeErr(w, http.StatusNotFound, "Dosyanın süresi doldu")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"name":       f.OriginalName,
		"size":       f.Size,
		"mime":       f.Mime,
		"ready":      true,
		"expires_at": f.ExpiresAt,
	})
}

func (sv *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	token := urlParam(r, "token")
	if !validToken(token) {
		writeErr(w, http.StatusBadRequest, "Geçersiz token")
		return
	}
	name := urlParam(r, "name")
	// chi {name} path parametresini URL-decode etmeden döndürebilir;
	// %20 gibi kodlanmış boşlukları elle çöz (dosya adlarında boşluk/özel karakter yaygın).
	if unescaped, err := url.PathUnescape(name); err == nil {
		name = unescaped
	}
	f, err := sv.store.GetFileByToken(token)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Sorgu hatası")
		return
	}
	if f == nil || f.Status != "ready" {
		writeErr(w, http.StatusNotFound, "Dosya bulunamadı veya hazır değil")
		return
	}
	if fileExpired(f) {
		writeErr(w, http.StatusNotFound, "Dosyanın süresi doldu")
		return
	}
	if f.OriginalName != name {
		writeErr(w, http.StatusNotFound, "İsim eşleşmiyor")
		return
	}
	parts, err := sv.store.GetParts(f.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Parçalar okunamadı")
		return
	}
	if len(parts) == 0 {
		writeErr(w, http.StatusConflict, "Bazı parçalar hazır değil")
		return
	}
	for _, p := range parts {
		if p.Status != "uploaded" {
			writeErr(w, http.StatusConflict, "Bazı parçalar hazır değil")
			return
		}
	}

	if !sv.waitWorker(w, r) {
		return
	}

	mime := f.Mime
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// NOT: Content-Length KASITLI olarak set edilmez. Handler flushWriter ile
	// chunked stream yapar; Content-Length + chunked kombinasyonu Cloudflare
	// Worker proxy'den geçerken body'nin kaybolmasına yol açar. (Parça kaybını
	// tarayıcı yine de algılar — HTTP/2 ve bağlantı kesintisi bunu işaretler.)
	// Video/resim/audio/PDF gibi inline gösterilebilen tipler tarayıcıda açılır;
	// diğerleri indirilir (attachment).
	disposition := "attachment"
	if isInlineType(mime) {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`,
			disposition, sanitizeHeaderName(f.OriginalName), url.PathEscape(f.OriginalName)))

	// flushWriter: parça parça akış
	fw := &flushWriter{w: w}
	fl, _ := w.(http.Flusher)
	if fl != nil {
		fl.Flush()
	}

	for _, p := range parts {
		if err := sv.tw.DownloadSegment(r.Context(), p, fw); err != nil {
			// akış başladıktan sonra status gönderilemez; bağlantıyı kes
			log.Printf("DownloadSegment hatası (file=%d token=%s part=%d/%d): %v",
				f.ID, f.Token, p.PartIndex, f.PartCount, err)
			return
		}
		if fl != nil {
			fl.Flush()
		}
	}
}

// isInlineType, tarayıcının doğrudan gösterebildiği MIME tiplerini döndürür.
// Video/resim/audio/PDF inline gösterilir; diğerleri indirme olarak davranır.
func isInlineType(mime string) bool {
	lower := strings.ToLower(mime)
	switch {
	case strings.HasPrefix(lower, "video/"):
		return true
	case strings.HasPrefix(lower, "image/"):
		return true
	case strings.HasPrefix(lower, "audio/"):
		return true
	case lower == "application/pdf":
		return true
	case strings.Contains(lower, "text/plain"):
		return true
	default:
		return false
	}
}

// sanitizeHeaderName, Content-Disposition için kontrol karakterlerini temizler.
func sanitizeHeaderName(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		if r >= 0x20 && r != '"' && r != '\\' && r != '\r' && r != '\n' {
			out = append(out, r)
		}
	}
	return string(out)
}

// flushWriter, io.Writer olarak yanıta yazar ve flush eder.
type flushWriter struct {
	w http.ResponseWriter
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}
