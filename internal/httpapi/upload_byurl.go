package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/proxy-turkey/hyaena-storage/internal/core"
)

type byURLBody struct {
	URL            string `json:"url"`
	Name           string `json:"name"`
	ExpiresInHours int    `json:"expires_in_hours"`
}

// knownExts, by-url isim türetmesinde uzantı korunacak tipler (Python birebir).
var knownExts = []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".pdf", ".txt", ".mp4", ".zip"}

// guardPublicURL, SSRF koruması: sadece genel http/https adresler.
func guardPublicURL(raw string) (string, error) {
	if raw == "" || len(raw) > 2048 {
		return "", fmt.Errorf("Geçersiz URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("Geçersiz URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("Yalnızca http/https adresler kabul edilir")
	}
	host := strings.ToLower(u.Hostname())
	// IP literali mi?
	if ip := net.ParseIP(host); ip != nil {
		// link-local: 169.254.0.0/16 (IPv4) veya fe80::/10 (IPv6)
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
			ip.IsUnspecified() || ip.IsMulticast() {
			return "", fmt.Errorf("Yerel adreslere izin verilmez")
		}
	} else if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return "", fmt.Errorf("Yerel adreslere izin verilmez")
	}
	return raw, nil
}

// deriveName, by-url için isim türetir: body.name veya URL basename.
func deriveName(nameHint, rawURL string) string {
	name := strings.TrimSpace(nameHint)
	if name == "" {
		if u, err := url.Parse(rawURL); err == nil {
			decoded, derr := url.PathUnescape(path.Base(u.Path))
			if derr == nil && decoded != "" && decoded != "/" && decoded != "." {
				name = decoded
			}
		}
	}
	if name == "" {
		name = "url-dosya"
	}
	name = core.SanitizeFilename(name)
	lower := strings.ToLower(name)
	known := false
	for _, e := range knownExts {
		if strings.HasSuffix(lower, e) {
			known = true
			break
		}
	}
	if !known {
		name += ".bin"
	}
	return name
}

func (sv *Server) uploadByURL(w http.ResponseWriter, r *http.Request) {
	var body byURLBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "Geçersiz istek")
		return
	}
	rawURL, err := guardPublicURL(body.URL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := deriveName(body.Name, rawURL)

	if sv.tw != nil {
		go func() { _ = sv.tw.EnsureFleet(sv.ctx) }()
	}
	chs, err := sv.store.ListChannels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Kanal listesi okunamadı")
		return
	}
	if len(chs) == 0 {
		writeErr(w, http.StatusServiceUnavailable, "Henüz depolama kanalı yok, birazdan tekrar dene")
		return
	}

	token := core.MakeShareToken()
	tmpDir := sv.fileTmpDir(token)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "Dizin oluşturulamadı")
		return
	}

	// Kanalları şimdi al (arka plan işi için)
	channelIDs := make([]int64, len(chs))
	for i, c := range chs {
		channelIDs[i] = c.ID
	}

	// Başlangıçta boyut bilinmiyor; part_count tahmini 1 olur.
	// İndirme bitince gerçek parça sayısı güncellenir ve upload başlar.
	pc := 1
	expires := expiresAtFor(body.ExpiresInHours)
	fid, err := sv.store.CreateFile(token, name, 0, "application/octet-stream", pc, expires)
	if err != nil {
		cleanupTmp(tmpDir)
		writeErr(w, http.StatusInternalServerError, "Dosya oluşturulamadı")
		return
	}
	if err := sv.store.CreatePendingParts(fid, pc); err != nil {
		cleanupTmp(tmpDir)
		writeErr(w, http.StatusInternalServerError, "Parça kayıtları oluşturulamadı")
		return
	}

	// İndirme + parçalama + Telegram upload → ARKA PLANDA (tarayıcı isteği anında kapanır)
	go func() {
		sv.downloadAndUpload(rawURL, name, token, tmpDir, fid, channelIDs)
	}()

	writeJSON(w, http.StatusOK, map[string]any{
		"token":         token,
		"name":          name,
		"size":          0,
		"part_count":    1,
		"segment_bytes": sv.s.SegmentBytes,
		"status":        "started",
	})
}

// downloadAndUpload, URL'den dosyayı indirir, parçalara böler ve Telegram'a yükler.
// Arka plan goroutine'inde çalışır — hatalar DB'ye 'failed' olarak yazılır.
func (sv *Server) downloadAndUpload(rawURL, name, token, tmpDir string, fid int64, channelIDs []int64) {
	client := &http.Client{
		Timeout: 30 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("çok fazla yönlendirme")
			}
			if _, err := guardPublicURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}

	fail := func(msg string) {
		_ = sv.store.SetFileFailed(fid, msg)
		cleanupTmp(tmpDir)
	}

	resp, err := client.Get(rawURL)
	if err != nil {
		fail(fmt.Sprintf("URL indirilemedi: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fail(fmt.Sprintf("URL yanıtı %d", resp.StatusCode))
		return
	}
	total := resp.ContentLength
	if total > sv.s.MaxUploadBytes {
		fail("Dosya çok büyük")
		return
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" || len(mime) > 200 {
		mime = "application/octet-stream"
	}
	if len(mime) > 200 {
		mime = mime[:200]
	}

	// gerçek parça sayısı
	pc := 1
	if total > 0 {
		pc = sv.partCount(total)
	}
	// part_count'u ve pending parçaları güncelle
	_ = sv.store.UpdateFileStatus(fid, "uploading", nil)
	_ = sv.store.UpdateFilePartCount(fid, pc)

	// stream indir → parça dosyalarına yaz
	idx := 0
	var out *os.File
	written := int64(0)
	buf := make([]byte, 512*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if total > 0 && written+int64(len(chunk)) > total {
				chunk = chunk[:total-written]
			}
			if out == nil {
				out, err = os.Create(segmentPath(tmpDir, idx))
				if err != nil {
					fail("Parça yazılamadı")
					return
				}
			}
			if _, werr := out.Write(chunk); werr != nil {
				out.Close()
				fail("Parça yazılamadı")
				return
			}
			written += int64(len(chunk))
			if written >= sv.s.SegmentBytes {
				out.Close()
				_ = sv.store.UpdatePartReceived(fid, idx, written)
				idx++
				written = 0
				out = nil
			}
			if total > 0 && written >= total {
				break
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if out != nil {
				out.Close()
			}
			fail(fmt.Sprintf("URL indirilemedi: %v", rerr))
			return
		}
	}
	if out != nil {
		out.Close()
		_ = sv.store.UpdatePartReceived(fid, idx, written)
		idx++
	}

	// gerçek boyut
	var realSize int64
	for i := 0; i < idx; i++ {
		if fi, err := os.Stat(segmentPath(tmpDir, i)); err == nil {
			realSize += fi.Size()
		}
	}
	if realSize == 0 {
		fail("URL'den veri alınamadı")
		return
	}
	_ = sv.store.UpdateFileSize(fid, realSize)
	_ = sv.store.UpdateFileMime(fid, mime)

	plan, err := sv.store.PickUploadChannelsBalanced(channelIDs, idx)
	if err != nil {
		fail("Kanal dağıtımı hesaplanamadı")
		return
	}
	segments := make([]string, 0, idx)
	for i := 0; i < idx; i++ {
		segments = append(segments, segmentPath(tmpDir, i))
	}

	if sv.tw == nil {
		fail("Telegram worker yok")
		return
	}
	_ = sv.tw.UploadSegments(sv.ctx, fid, tmpDir, segments, plan, mime)
}
