package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/proxy-turkey/hyaena-storage/internal/core"
)

func (sv *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(sv.s.AdminPassword)) != 1 {
		writeErr(w, http.StatusUnauthorized, "Yanlış şifre")
		return
	}
	tok := core.MakeAdminToken(sv.s.TokenSecret(), sv.s.TokenTTLHours, time.Now())
	http.SetCookie(w, &http.Cookie{
		Name:     sv.s.AdminCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   sv.s.TokenTTLHours * 3600,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (sv *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sv.s.AdminCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (sv *Server) adminSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": sv.verifyAdmin(r)})
}

func (sv *Server) adminSummary(w http.ResponseWriter, r *http.Request) {
	totalFiles, _ := sv.store.CountFiles()
	totalBytes, _ := sv.store.StorageUsage()
	todayFiles, _ := sv.store.CountFilesToday()
	chs, _ := sv.store.ListChannels()
	writeJSON(w, http.StatusOK, map[string]any{
		"total_files":    totalFiles,
		"total_bytes":    totalBytes,
		"today_files":    todayFiles,
		"channels_count": len(chs),
		"segment_bytes":  sv.s.SegmentBytes,
		"max_upload_bytes": sv.s.MaxUploadBytes,
	})
}

func (sv *Server) adminChannels(w http.ResponseWriter, r *http.Request) {
	chs, err := sv.store.ListChannels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Kanal listesi okunamadı")
		return
	}
	out := make([]map[string]any, 0, len(chs))
	for _, c := range chs {
		out = append(out, map[string]any{
			"id":          c.ID,
			"telegram_id": c.TelegramID,
			"title":       c.Title,
			"created_day": c.CreatedDay,
			"created_at":  c.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out})
}

func (sv *Server) adminCreateChannel(w http.ResponseWriter, r *http.Request) {
	if !sv.waitWorker(w, r) {
		return
	}
	err := sv.tw.EnsureDailyChannel(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"created": true})
}

// publicBase, download linklerinin tabanını döndürür. Config'te PublicBaseURL
// varsa mutlak URL, yoksa boş string (göreceli /api/download/... — eski davranış).
func (sv *Server) publicBase() string {
	return sv.s.PublicBaseURL
}

func (sv *Server) adminFiles(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	files, err := sv.store.ListFiles(limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Dosyalar okunamadı")
		return
	}
	total, _ := sv.store.CountFiles()
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		parts, _ := sv.store.GetParts(f.ID)
		// blob'u admin yanıtında gizle (frontend ihtiyaç duymaz)
		partList := make([]map[string]any, 0, len(parts))
		for _, p := range parts {
			partList = append(partList, map[string]any{
				"id":              p.ID,
				"part_index":      p.PartIndex,
				"channel_id":      p.ChannelID,
				"telegram_msg_id": p.TelegramMsgID,
				"size":            p.Size,
				"status":          p.Status,
				"error":           p.Error,
				"uploaded_at":     p.UploadedAt,
			})
		}
		out = append(out, map[string]any{
			"id":            f.ID,
			"token":         f.Token,
			"original_name": f.OriginalName,
			"size":          f.Size,
			"mime":          f.Mime,
			"part_count":    f.PartCount,
			"done_parts":    f.DoneParts,
			"status":        f.Status,
			"error":         f.Error,
			"created_at":    f.CreatedAt,
			"ready_at":      f.ReadyAt,
			"parts":         partList,
			// Boşluk/#/özel karakter içeren adlar linki kırmasın diye URL-encode edilir.
			// Download'lar doğrudan orfi sunucusuna gider (Cloudflare büyük dosyaları keser).
			"download_url":  sv.publicBase() + "/api/download/" + f.Token + "/" + url.PathEscape(f.OriginalName),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": out, "total": total})
}

func (sv *Server) adminDeleteFile(w http.ResponseWriter, r *http.Request) {
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
	deleted := 0
	for _, p := range parts {
		if p.TelegramMsgID != nil && sv.tw != nil {
			_ = sv.tw.DeleteSegmentMessage(r.Context(), p)
			deleted++
		}
	}
	_ = sv.store.DeleteFile(f.ID)
	cleanupTmp(sv.fileTmpDir(token))
	// Cloudflare edge cache'ini temizle (silinen dosya erişilebilir kalmasın).
	// Admin yanıtını bekletmesin — purge arka planda yapılır.
	if sv.s.CFZoneID != "" && sv.s.CFAPIKey != "" {
		base := "https://storage.hyaena.co.uk"
		// İsim URL-encode edilir (açık purge isteğinin aynısı olmalı).
		u1 := base + "/api/download/" + token + "/" + url.PathEscape(f.OriginalName)
		u2 := base + "/api/download/" + token
		go func() {
			sv.purgeCacheFile(u1)
			sv.purgeCacheFile(u2)
		}()
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "parts_deleted": len(parts)})
}

func (sv *Server) adminResync(w http.ResponseWriter, r *http.Request) {
	token := urlParam(r, "token")
	if !validToken(token) {
		writeErr(w, http.StatusBadRequest, "Geçersiz token")
		return
	}
	var body struct {
		PartID int64 `json:"part_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.PartID <= 0 {
		writeErr(w, http.StatusBadRequest, "part_id gerekli")
		return
	}
	if !sv.waitWorker(w, r) {
		return
	}
	ok, err := sv.tw.ResyncPart(r.Context(), body.PartID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusConflict, "Resync başarısız")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resynced"})
}

func (sv *Server) adminSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"segment_bytes":       sv.s.SegmentBytes,
		"max_upload_bytes":    sv.s.MaxUploadBytes,
		"rate_limit_per_min":  sv.s.RateLimitPerMin,
		"channel_interval_sn": sv.s.ChannelIntervalSN,
		"inter_message_sleep": sv.s.InterMessageSleep,
	})
}
