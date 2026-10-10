package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/proxy-turkey/hyaena-storage/internal/core"
)

// tokenLen, valid_token eşiği (core ile tutarlı).
const tokenLen = 20

// partCount, size'ın segment sayısı.
func (sv *Server) partCount(size int64) int {
	return core.PartCount(size, sv.s.SegmentBytes)
}

// fileTmpDir, token için geçici dizini döndürür (token doğrulanmış olmalı).
func (sv *Server) fileTmpDir(token string) string {
	return filepath.Join(sv.s.TmpDir(), token)
}

// segmentPath, parça dosyasının yolunu döndürür (6 haneli sıfır dolgulu).
func segmentPath(tmpDir string, index int) string {
	return filepath.Join(tmpDir, fmt.Sprintf("part_%06d.bin", index))
}

// cleanupTmp, geçici dizini siler.
func cleanupTmp(tmpDir string) {
	_ = os.RemoveAll(tmpDir)
}

// validToken, paylaşım token'ını doğrular.
func validToken(tok string) bool {
	return core.ValidShareToken(tok)
}

// ---------- Upload start ----------

type uploadStartBody struct {
	Name           string `json:"name"`
	Size           int64  `json:"size"`
	Mime           string `json:"mime"`
	ExpiresInHours int    `json:"expires_in_hours"` // 0 = süresiz
}

// expiresAtFor, süreyi saat cinsinden alır ve son kullanma zamanı döndürür.
// saat 0 ise boş string (süresiz) döner.
// Zaman damgası UTC'dir (SQLite datetime('now') ile tutarlı).
func expiresAtFor(hours int) string {
	if hours <= 0 {
		return ""
	}
	// desteklenen süreler: 1 saat, 1 gün, 1 hafta, 1 ay
	switch hours {
	case 1, 24, 168, 720:
		// geçerli
	default:
		return ""
	}
	return time.Now().UTC().Add(time.Duration(hours) * time.Hour).Format("2006-01-02 15:04:05")
}

func (sv *Server) uploadStart(w http.ResponseWriter, r *http.Request) {
	// Metadata gövdesi asla büyük olmamalı: decode'dan ÖNCE üst sınır (F-04) —
	// sınırsız JSON decode tek istekle süreci OOM eder.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body uploadStartBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "Geçersiz istek")
		return
	}
	name := core.SanitizeFilename(body.Name)
	size := body.Size
	if size < 0 {
		writeErr(w, http.StatusBadRequest, "Geçersiz boyut")
		return
	}
	if size > sv.s.MaxUploadBytes {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("Dosya çok büyük (max %d byte)", sv.s.MaxUploadBytes))
		return
	}
	mime := sanitizeMime(body.Mime)

	// kanal filosu hazır mı? (tek seferlik arka plan garantisi)
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

	// Toplam in-flight tavanı (F-05): eşzamanlı 'uploading' ilan boyutlarının
	// toplamı + yeni dosya tavanı aşamaz — tek dosya sınırı tek başına diski korumaz.
	if sv.s.MaxInflightBytes > 0 {
		inflight, ierr := sv.store.InflightBytes()
		if ierr == nil && inflight+size > sv.s.MaxInflightBytes {
			writeErr(w, http.StatusServiceUnavailable, "Sunucu upload kapasitesi dolu, birazdan tekrar dene")
			return
		}
	}

	token := core.MakeShareToken()
	pc := sv.partCount(size)
	expires := expiresAtFor(body.ExpiresInHours)
	fid, err := sv.store.CreateFile(token, name, size, mime, pc, expires)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Dosya oluşturulamadı")
		return
	}
	if err := sv.store.CreatePendingParts(fid, pc); err != nil {
		writeErr(w, http.StatusInternalServerError, "Parça kayıtları oluşturulamadı")
		return
	}
	tmpDir := sv.fileTmpDir(token)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "Geçici dizin oluşturulamadı")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"token":            token,
		"segment_bytes":    sv.s.SegmentBytes,
		"part_count":       pc,
		"max_upload_bytes": sv.s.MaxUploadBytes,
		// Temizlenmiş ad: frontend linki/görüntüyü bu adla kurmalı (yoksa
		// ham ad depolanan adla eşleşmez → indirme 404).
		"name": name,
	})
}

// ---------- Upload part ----------

func (sv *Server) uploadPart(w http.ResponseWriter, r *http.Request) {
	token := urlParam(r, "token")
	if !validToken(token) {
		writeErr(w, http.StatusBadRequest, "Geçersiz token")
		return
	}
	index, err := strconv.Atoi(urlParam(r, "index"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Geçersiz parça indeksi")
		return
	}
	f, err := sv.store.GetFileByToken(token)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Sorgu hatası")
		return
	}
	if f == nil {
		writeErr(w, http.StatusNotFound, "Dosya bulunamadı")
		return
	}
	if f.Status != "uploading" {
		writeErr(w, http.StatusConflict, "Upload zaten tamamlandı")
		return
	}
	if index < 0 || index >= f.PartCount {
		writeErr(w, http.StatusBadRequest, "Parça indeksi aralık dışı")
		return
	}

	tmpDir := sv.fileTmpDir(token)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "Dizin oluşturulamadı")
		return
	}
	// Parça boyutu segment_bytes ile sınırlıdır — diski dolduracak devasa
	// gönderimleri önler (DoS koruması). Son parça dahil her parça en fazla
	// segment_bytes olabilir.
	maxPart := sv.s.SegmentBytes
	// son parçanın beklenen maks boyutu: size - (part_count-1)*segment_bytes
	if index == f.PartCount-1 {
		expected := f.Size - int64(f.PartCount-1)*sv.s.SegmentBytes
		if expected > 0 && expected < maxPart {
			maxPart = expected
		}
	}

	// Serbest disk kapısı (F-05): parça + güvenlik payı kadar yer yoksa yazma.
	if free, ferr := core.DiskFreeBytes(tmpDir); ferr == nil && free < uint64(maxPart)+(16<<20) {
		writeErr(w, http.StatusInsufficientStorage, "Disk dolu")
		return
	}

	path := segmentPath(tmpDir, index)
	out, err := os.Create(path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("Parça yazılamadı: %v", err))
		return
	}
	// sınırlı kopyalama: maxPart+1 bayt oku (aşımı tespit et)
	limited := io.LimitReader(r.Body, maxPart+1)
	received, err := io.Copy(out, limited)
	cerr := out.Close()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("Parça yazılamadı: %v", err))
		return
	}
	if cerr != nil {
		writeErr(w, http.StatusInternalServerError, "Parça kapatılamadı")
		return
	}
	if received > maxPart {
		// parça çok büyük — diski boşa kullanma, temizle
		_ = os.Remove(path)
		writeErr(w, http.StatusBadRequest, "Parça boyutu segment limitini aşıyor")
		return
	}
	if err := sv.store.UpdatePartReceived(f.ID, index, received); err != nil {
		writeErr(w, http.StatusInternalServerError, "Kayıt güncellenemedi")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"received": received})
}

// ---------- Upload finish ----------

func (sv *Server) uploadFinish(w http.ResponseWriter, r *http.Request) {
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
	if f == nil {
		writeErr(w, http.StatusNotFound, "Dosya bulunamadı")
		return
	}
	if f.Status != "uploading" {
		writeErr(w, http.StatusConflict, "Upload zaten işleniyor/tamamlandı")
		return
	}

	tmpDir := sv.fileTmpDir(token)
	// Herhangi bir hata yolu tmp'yi temizlesin (volume asla dolmasın)
	cleanupOnErr := func(status int, detail string) {
		cleanupTmp(tmpDir)
		writeErr(w, status, detail)
	}
	segments := make([]string, 0, f.PartCount)
	for i := 0; i < f.PartCount; i++ {
		p := segmentPath(tmpDir, i)
		if _, err := os.Stat(p); err != nil {
			cleanupOnErr(http.StatusBadRequest, fmt.Sprintf("Parça %d eksik", i))
			return
		}
		segments = append(segments, p)
	}

	chs, err := sv.store.ListChannels()
	if err != nil {
		cleanupOnErr(http.StatusInternalServerError, "Kanal listesi okunamadı")
		return
	}
	if len(chs) == 0 {
		cleanupOnErr(http.StatusServiceUnavailable, "Depolama kanalı yok")
		return
	}
	channelIDs := make([]int64, len(chs))
	for i, c := range chs {
		channelIDs[i] = c.ID
	}
	plan, err := sv.store.PickUploadChannelsBalanced(channelIDs, f.PartCount)
	if err != nil {
		cleanupOnErr(http.StatusInternalServerError, "Kanal dağıtımı hesaplanamadı")
		return
	}

	if !sv.waitWorker(w, r) {
		cleanupTmp(tmpDir)
		return
	}
	// Çift finish (çift tıklama / yarış) aynı dosyayı iki kez Telegram'a
	// yükletip eş mesajlar bırakır — token başına tek başlatmaya izin ver.
	if !sv.tryStartFinish(token) {
		cleanupTmp(tmpDir)
		writeErr(w, http.StatusConflict, "Upload zaten başlatıldı")
		return
	}
	// Arka plan upload'ı güvenli sarmalayıcıyla başlat: panic process'i
	// çökertmesin ve hata dosyayı 'uploading'de takılı bırakmasın.
	// Bağlam iptal edilebilir (F-14): cancel, uçuştaki dağıtımı durdurur.
	go func() {
		defer sv.finishDone(token)
		upCtx, cancel := sv.registerUploadCancel(f.ID)
		defer cancel()
		defer sv.unregisterUploadCancel(f.ID)
		sv.uploadSegmentsSafe(upCtx, f.ID, tmpDir, segments, plan, f.Mime)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

// tryStartFinish, token için finish'i atomik olarak ilk kez işaretler.
// İkinci ve sonraki çağrılar false döner.
func (sv *Server) tryStartFinish(token string) bool {
	sv.finishMu.Lock()
	defer sv.finishMu.Unlock()
	if _, ok := sv.finishStarted[token]; ok {
		return false
	}
	sv.finishStarted[token] = struct{}{}
	return true
}

// finishDone, finish işaretini kaldırır (retry iptal/temizliği için).
func (sv *Server) finishDone(token string) {
	sv.finishMu.Lock()
	defer sv.finishMu.Unlock()
	delete(sv.finishStarted, token)
}

// ---------- Upload status ----------

func (sv *Server) uploadStatus(w http.ResponseWriter, r *http.Request) {
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
	if f == nil {
		writeErr(w, http.StatusNotFound, "Dosya bulunamadı")
		return
	}
	parts, _ := sv.store.GetParts(f.ID)
	var received int64
	for _, p := range parts {
		received += p.Size
	}
	var tgProgress map[string]any
	if sv.tw != nil {
		tgProgress = sv.tw.GetUploadStatus(f.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":            f.Status,
		"received_bytes":    received,
		"part_count":        f.PartCount,
		"done_parts":        f.DoneParts,
		"error":             f.Error,
		"telegram_progress": tgProgress,
	})
}

// ---------- Upload cancel ----------

func (sv *Server) uploadCancel(w http.ResponseWriter, r *http.Request) {
	token := urlParam(r, "token")
	if !validToken(token) {
		writeErr(w, http.StatusBadRequest, "Geçersiz token")
		return
	}
	f, err := sv.store.GetFileByToken(token)
	if err == nil && f != nil && f.Status == "uploading" {
		// F-14: satır düşmeden ÖNCE kayıtlı segmentlerin Telegram mesajları
		// silinir (cascade msg_id'leri yok eder — sonrasında kurtarılamaz);
		// ardından uçuştaki arka plan dağıtımı iptal edilir.
		if parts, perr := sv.store.GetParts(f.ID); perr == nil && sv.tw != nil {
			for _, p := range parts {
				if p.TelegramMsgID != nil {
					if derr := sv.tw.DeleteSegmentMessage(r.Context(), p); derr != nil {
						log.Printf("İptal: segment mesajı silinemedi (file=%d part=%d): %v", f.ID, p.PartIndex, derr)
					}
				}
			}
		}
		sv.cancelUploadSignal(f.ID)
		_ = sv.store.DeleteFile(f.ID)
	}
	sv.finishDone(token) // yarım kalmış bir finish işaretinin kilidini bırak
	cleanupTmp(sv.fileTmpDir(token))
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// uploadSegmentsSafe, arka plan Telegram upload'ını panic-korumalı ve
// hata-bilinçli çalıştırır. Panic process'i çökertmesin (Recoverer kapsamaz),
// erken hata dosyayı 'uploading'de takılı bırakmasın.
func (sv *Server) uploadSegmentsSafe(ctx context.Context, fileID int64, tmpDir string, segments []string, plan []int64, mime string) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("Upload goroutine panic (file=%d): %v", fileID, rec)
			if f, err := sv.store.GetFileByID(fileID); err == nil && f != nil && f.Status == "uploading" {
				_ = sv.store.SetFileFailed(fileID, "iç hata")
			}
		}
	}()
	if err := sv.tw.UploadSegments(ctx, fileID, tmpDir, segments, plan, mime); err != nil {
		// UploadSegments içeride parça hatasında SetFileFailed yapar; buraya
		// sadece çok-erken hatalar (apiGuard, kanal yok) düşer.
		log.Printf("Telegram upload hatası (file=%d): %v", fileID, err)
		if f, gerr := sv.store.GetFileByID(fileID); gerr == nil && f != nil && f.Status == "uploading" {
			_ = sv.store.SetFileFailed(fileID, err.Error())
		}
	}
}

// registerUploadCancel, uçuştaki arka plan upload'u için iptal edilebilir
// bağlam kaydeder (F-14). Dönen cancel her durumda çağrılmalı.
func (sv *Server) registerUploadCancel(fileID int64) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(sv.ctx)
	sv.uploadMu.Lock()
	sv.uploadCancels[fileID] = cancel
	sv.uploadMu.Unlock()
	return ctx, cancel
}

// unregisterUploadCancel, kaydı temizler (upload tamamlandı/hiba).
func (sv *Server) unregisterUploadCancel(fileID int64) {
	sv.uploadMu.Lock()
	delete(sv.uploadCancels, fileID)
	sv.uploadMu.Unlock()
}

// cancelUploadSignal, kayıtlı uçuşlu upload varsa iptal eder (F-14).
func (sv *Server) cancelUploadSignal(fileID int64) {
	sv.uploadMu.Lock()
	cancel, ok := sv.uploadCancels[fileID]
	if ok {
		delete(sv.uploadCancels, fileID)
	}
	sv.uploadMu.Unlock()
	if ok {
		cancel()
	}
}
