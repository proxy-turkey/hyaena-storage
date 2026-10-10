package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/proxy-turkey/hyaena-storage/internal/config"
	"time"
)

// PurgeCacheFile, Cloudflare edge cache'inden dosya URL'sini temizler —
// settings tabanlı bağımsız fonksiyon (scheduler'ın expiry purge'u da kullanır, F-07).
func PurgeCacheFile(s *config.Settings, fileURL string) error {
	if s.CFZoneID == "" || (s.CFAPIToken == "" && s.CFAPIKey == "") {
		return nil // Cloudflare purge yapılandırılmamış
	}
	body, _ := json.Marshal(map[string]any{"files": []string{fileURL}})
	req, err := http.NewRequest(
		"POST",
		"https://api.cloudflare.com/client/v4/zones/"+s.CFZoneID+"/purge_cache",
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// Scoped API token tercih edilir (Bearer); Global Key yalnızca fallback —
	// kimlik bilgisi hijyeni (B-03): token tek amaca (cache purge) kısıtlı.
	if s.CFAPIToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.CFAPIToken)
	} else {
		req.Header.Set("X-Auth-Email", s.CFAPIEmail)
		req.Header.Set("X-Auth-Key", s.CFAPIKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == 200 {
		log.Printf("Cloudflare cache purge edildi: %s", fileURL)
		return nil
	}
	return fmt.Errorf("purge HTTP %d: %s", resp.StatusCode, fileURL)
}

// purgeCacheFile, Server metodu sarmalayıcısı (admin delete yolu).
func (sv *Server) purgeCacheFile(fileURL string) {
	_ = PurgeCacheFile(sv.s, fileURL)
}
