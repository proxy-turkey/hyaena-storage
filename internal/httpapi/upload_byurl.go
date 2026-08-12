package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

// isBlockedIP, SSRF koruması: özel/loopback/link-local/yerel IP'leri reddeder.
// IPv4-mapped IPv6 (::ffff:127.0.0.1) formu da kontrol edilir.
func isBlockedIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		return v4.IsPrivate() || v4.IsLoopback() || v4.IsLinkLocalUnicast() ||
			v4.IsUnspecified() || v4.IsMulticast()
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalMulticast()
}

// guardPublicURL, SSRF koruması: sadece genel http/https adresler.
// IP literali, localhost ve bilinen yerel hostname'ler reddedilir.
func guardPublicURL(raw string) error {
	if raw == "" || len(raw) > 2048 {
		return fmt.Errorf("Geçersiz URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("Geçersiz URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("Yalnızca http/https adresler kabul edilir")
	}
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("Yerel adreslere izin verilmez")
		}
		return nil
	}
	// Hostname: yerel anlamı olan adlar
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "metadata.google.internal" ||
		strings.HasSuffix(host, ".localhost") || host == "0" {
		return fmt.Errorf("Yerel adreslere izin verilmez")
	}
	return nil
}

// ssrfDialContext, http.Client için özel dial: bağlantı anında hostname'i çözer
// ve tüm çözülen IP'ler genel değilse reddeder (DNS rebinding + decimal/hex/octal
// IP formları dahil — net/url bunları hostname olarak yorumlar).
func ssrfDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("çözümlenebilir adres yok")
	}
	for _, ia := range ips {
		if isBlockedIP(ia.IP) {
			return nil, fmt.Errorf("yerel adrese bağlanılamaz: %s", ia.IP)
		}
	}
	d := &net.Dialer{Timeout: 15 * time.Second}
	return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
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
	// uzantı koruması: bilinen tipler dokunulmaz; bilinmeyenlerde yalnızca
	// isimde hiç nokta yoksa ".bin" eklenir (song.mp3 → song.mp3, asla song.mp3.bin).
	lower := strings.ToLower(name)
	for _, e := range knownExts {
		if strings.HasSuffix(lower, e) {
			return name
		}
	}
	if !strings.Contains(name, ".") {
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
	if err := guardPublicURL(body.URL); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rawURL := body.URL
	name := deriveName(body.Name, rawURL)

	sv.ensureFleet()
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
		Timeout:   30 * time.Minute,
		Transport: &http.Transport{DialContext: ssrfDialContext},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("çok fazla yönlendirme")
			}
			return guardPublicURL(req.URL.String())
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
	if err := sv.tw.UploadSegments(sv.ctx, fid, tmpDir, segments, plan, mime); err != nil {
		// UploadSegments içeride parça hatasında SetFileFailed yapar; buraya
		// sadece çok-erken hatalar (apiGuard, kanal yok) düşer — dosya takılmasın.
		log.Printf("By-url Telegram upload hatası (file=%d): %v", fid, err)
		if f, gerr := sv.store.GetFileByID(fid); gerr == nil && f != nil && f.Status == "uploading" {
			_ = sv.store.SetFileFailed(fid, err.Error())
		}
		cleanupTmp(tmpDir)
	}
}
