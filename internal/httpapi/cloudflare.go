package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// purgeCacheFile, Cloudflare Worker edge cache'inden dosya URL'sini temizler.
// Dosya silinince cache'te kalmaması için admin delete'e entegre edilir.
func (sv *Server) purgeCacheFile(fileURL string) {
	s := sv.s
	if s.CFZoneID == "" || s.CFAPIKey == "" {
		return // Cloudflare purge yapılandırılmamış
	}
	body, _ := json.Marshal(map[string]any{"files": []string{fileURL}})
	req, err := http.NewRequest(
		"POST",
		"https://api.cloudflare.com/client/v4/zones/"+s.CFZoneID+"/purge_cache",
		bytes.NewReader(body),
	)
	if err != nil {
		return
	}
	req.Header.Set("X-Auth-Email", s.CFAPIEmail)
	req.Header.Set("X-Auth-Key", s.CFAPIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == 200 {
		fmt.Printf("Cloudflare cache purge edildi: %s\n", fileURL)
	}
}
