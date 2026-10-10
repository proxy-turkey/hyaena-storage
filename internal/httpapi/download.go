package httpapi

import (
	"bytes"
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

	// Eşzamanlı indirme sınırı: ilk parça bellekte tutulduğu için sınırsız
	// eşzamanlılık küçük RAM'de OOM yapabilir. Semafor tüm akış boyunca tutulur.
	if sv.dlSem != nil {
		select {
		case sv.dlSem <- struct{}{}:
			defer func() { <-sv.dlSem }()
		default:
			writeErr(w, http.StatusServiceUnavailable, "Çok fazla eşzamanlı indirme, birazdan tekrar dene")
			return
		}
	}

	// İlk parçayı başlıklardan ÖNCE belleğe indir (segment ≤ 20 MB): parçanın
	// file reference'ı süresi dolmuşsa resync bu aşamada tamamlanır. Böylece
	// istemciye asla 200 + boş gövde gitmez — Cloudflare edge boş 200'leri
	// 30 gün cache'lediği için bu, kalıcı kırık indirmelere yol açıyordu.
	var first []byte
	{
		var buf bytes.Buffer
		if err := sv.tw.DownloadSegment(r.Context(), parts[0], &buf); err != nil {
			log.Printf("Download ilk parça hatası (file=%d token=%s part=%d/%d): %v",
				f.ID, f.Token, parts[0].PartIndex, f.PartCount, err)
			writeErr(w, http.StatusBadGateway, "Dosya Telegram'dan alınamadı")
			return
		}
		first = buf.Bytes()
	}

	mime := f.Mime
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// F-01 savunma katmanı: indirme yanıtları asla betik/çerçeve çalıştırmaz.
	w.Header().Set("Content-Security-Policy", "sandbox")
	// F-07: edge TTL dosyanın kalan ömrünü aşamaz — Worker bu s-maxage'i
	// onurlanır; süresiz dosyalarda 30 günlük üst sınır geçerli kalır.
	cc := "public, s-maxage=2592000"
	if f.ExpiresAt != nil && *f.ExpiresAt != "" {
		if t, perr := time.Parse("2006-01-02 15:04:05", *f.ExpiresAt); perr == nil {
			if rem := int(time.Until(t).Seconds()); rem >= 0 && rem < 2592000 {
				cc = fmt.Sprintf("public, s-maxage=%d", rem)
			}
		}
	}
	w.Header().Set("Cache-Control", cc)
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

	// F-09: parça bazlı idle-write deadline — gövdeyi okumayan/damlatan
	// istemci bu süre içinde hiç bayt almazsa bağlantı kesilir, dlSem slotu
	// ve ilk segment belleği serbest kalır. ≥128 KiB/s aktif okuyucu düşmez.
	rc := http.NewResponseController(w)
	segBytes := int64(sv.s.SegmentBytes)
	perPart := time.Duration(segBytes/(128*1024)) * time.Second
	if perPart < 30*time.Second {
		perPart = 30 * time.Second
	}
	renew := func() { _ = rc.SetWriteDeadline(time.Now().Add(perPart)) }

	// ilk parça zaten bellekte; doğrudan yaz ve kalanları akıt
	renew()
	if _, err := fw.Write(first); err != nil {
		return
	}
	if fl != nil {
		fl.Flush()
	}
	for _, p := range parts[1:] {
		renew()
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
// isInlineType: YALNIZCA betik çalıştıramayan tipler inline kalır (F-01).
// image/svg+xml ve tüm text/* bilinçli olarak HARİÇTİR: SVG top-level
// navigasyonda betik çalıştırır (stored XSS); text/html sniffing riski.
func isInlineType(mime string) bool {
	lower := strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0]))
	switch lower {
	case "image/gif", "image/png", "image/jpeg", "image/webp", "image/bmp",
		"video/mp4", "video/webm", "video/ogg",
		"audio/mpeg", "audio/ogg", "audio/wav", "audio/webm",
		"application/pdf":
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
