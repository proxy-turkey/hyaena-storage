package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"tgshare/internal/core"
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
	var body uploadStartBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "Geçersiz istek")
		return
	}
	name := core.SanitizeFilename(body.Name)
	size := body.Size
	if size <= 0 {
		writeErr(w, http.StatusBadRequest, "Boş dosya olamaz")
		return
	}
	if size > sv.s.MaxUploadBytes {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("Dosya çok büyük (max %d byte)", sv.s.MaxUploadBytes))
		return
	}
	mime := body.Mime
	if mime == "" || len(mime) > 200 {
		mime = "application/octet-stream"
	}
	if len(mime) > 200 {
		mime = mime[:200]
	}

	// kanal filosu hazır mı?
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
		"token":           token,
		"segment_bytes":   sv.s.SegmentBytes,
		"part_count":      pc,
		"max_upload_bytes": sv.s.MaxUploadBytes,
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
	segments := make([]string, 0, f.PartCount)
	for i := 0; i < f.PartCount; i++ {
		p := segmentPath(tmpDir, i)
		if _, err := os.Stat(p); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("Parça %d eksik", i))
			return
		}
		segments = append(segments, p)
	}

	chs, err := sv.store.ListChannels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Kanal listesi okunamadı")
		return
	}
	if len(chs) == 0 {
		writeErr(w, http.StatusServiceUnavailable, "Depolama kanalı yok")
		return
	}
	channelIDs := make([]int64, len(chs))
	for i, c := range chs {
		channelIDs[i] = c.ID
	}
	plan, err := sv.store.PickUploadChannelsBalanced(channelIDs, f.PartCount)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Kanal dağıtımı hesaplanamadı")
		return
	}

	if !sv.waitWorker(w, r) {
		return
	}
	go func() {
		_ = sv.tw.UploadSegments(sv.ctx, f.ID, tmpDir, segments, plan, f.Mime)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
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
		"status":          f.Status,
		"received_bytes":  received,
		"part_count":      f.PartCount,
		"done_parts":      f.DoneParts,
		"error":           f.Error,
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
		_ = sv.store.DeleteFile(f.ID)
	}
	cleanupTmp(sv.fileTmpDir(token))
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}
