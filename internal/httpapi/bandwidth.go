package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// nfMetricSeries, Northflank metrics yanıtındaki bir metric.
type nfMetricSeries struct {
	Values []struct {
		Metadata struct {
			ContainerID string `json:"containerId"`
		} `json:"metadata"`
		Data []struct {
			Ts    string  `json:"ts"`
			Value float64 `json:"value"`
		} `json:"data"`
	} `json:"values"`
}

// northflankMetrics, Northflank metrics API'sinden bir metric çeker.
func (sv *Server) northflankMetrics(metricType string) (map[string]nfMetricSeries, error) {
	s := sv.s
	if s.NFToken == "" {
		return nil, fmt.Errorf("NORTHFLANK_TOKEN yok")
	}
	now := time.Now().UTC()
	from := now.Add(-24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	to := now.Format("2006-01-02T15:04:05.000Z")
	url := fmt.Sprintf(
		"https://api.northflank.com/v1/projects/%s/services/%s/metrics?metricTypes=%s&queryType=range&startTime=%s&endTime=%s",
		s.NFProject, s.NFService, metricType, from, to,
	)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+s.NFToken)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Data map[string]nfMetricSeries `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return result.Data, nil
}

// bandwidthData, /bandwidth için hesaplanmış egress verisi.
type bandwidthData struct {
	TotalKB     float64
	TotalGB     float64
	LatestKbps  float64
	LimitGB     float64
	UsedPercent float64
	VolErr      string
	EgrErr      string
	FetchedAt   string
	Project     string
	Service     string
	Source      string // veri kaynağı: "cloudflare" veya "northflank"
}

// cloudflareBandwidthBytes, Cloudflare GraphQL Analytics'ten son days günün
// toplam indirilen bytes değerini döndürür. Gerçek trafik Cloudflare edge
// cache'inden servis edildiği için asıl kullanım buradadır (Northflank ~0 kalır).
func (sv *Server) cloudflareBandwidthBytes(days int) (int64, error) {
	s := sv.s
	if s.CFZoneID == "" || s.CFAPIKey == "" || s.CFAPIEmail == "" {
		return 0, fmt.Errorf("Cloudflare API yapılandırılmamış")
	}
	from := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	query := fmt.Sprintf(`{ viewer { zones(filter: {zoneTag: "%s"}) { httpRequests1dGroups(limit: %d, filter: {date_geq: "%s"}) { sum { bytes } } } } }`,
		s.CFZoneID, days, from)

	body, _ := json.Marshal(map[string]string{"query": query})
	req, _ := http.NewRequest("POST", "https://api.cloudflare.com/client/v4/graphql", strings.NewReader(string(body)))
	req.Header.Set("X-Auth-Email", s.CFAPIEmail)
	req.Header.Set("X-Auth-Key", s.CFAPIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var result struct {
		Data struct {
			Viewer struct {
				Zones []struct {
					HTTPRequests1dGroups []struct {
						Sum struct {
							Bytes int64 `json:"bytes"`
						} `json:"sum"`
					} `json:"httpRequests1dGroups"`
				} `json:"zones"`
			} `json:"viewer"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, err
	}
	if len(result.Errors) > 0 {
		return 0, fmt.Errorf("Cloudflare GraphQL: %s", result.Errors[0].Message)
	}
	var total int64
	for _, z := range result.Data.Viewer.Zones {
		for _, g := range z.HTTPRequests1dGroups {
			total += g.Sum.Bytes
		}
	}
	return total, nil
}

func (sv *Server) collectBandwidth() bandwidthData {
	d := bandwidthData{LimitGB: 10, FetchedAt: time.Now().UTC().Format("2006-01-02 15:04:05"), Project: sv.s.NFProject, Service: sv.s.NFService}

	// Toplam kullanım: Cloudflare Analytics (gerçek indirme trafiği edge cache'ten
	// geçtiği için asıl sayı burasıdır). CF başarısızsa Northflank bandwidthVolume'a düş.
	cfBytes, cfErr := sv.cloudflareBandwidthBytes(30)
	if cfErr == nil {
		d.TotalKB = float64(cfBytes) / 1024.0
		d.Source = "cloudflare"
	} else {
		d.VolErr = cfErr.Error()
		vol, volErr := sv.northflankMetrics("bandwidthVolume")
		if volErr == nil {
			if md, ok := vol["bandwidthVolume"]; ok {
				for _, c := range md.Values {
					for _, p := range c.Data {
						d.TotalKB += p.Value
					}
				}
			}
			d.Source = "northflank"
		} else {
			d.VolErr = volErr.Error()
		}
	}

	// Anlık egress hızı: Northflank networkEgress (kbps) — son container'ların
	// son değerleri toplamı. 5dk gecikmeli ama gerçek; sabit/yanlış değil.
	egr, egrErr := sv.northflankMetrics("networkEgress")
	if egrErr == nil {
		if md, ok := egr["networkEgress"]; ok {
			latest := 0.0
			for _, c := range md.Values {
				if len(c.Data) > 0 {
					latest += c.Data[len(c.Data)-1].Value
				}
			}
			d.LatestKbps = latest
		}
	} else {
		d.EgrErr = egrErr.Error()
	}

	d.TotalGB = d.TotalKB / (1024 * 1024)
	d.UsedPercent = (d.TotalGB / d.LimitGB) * 100
	return d
}

// bandwidthPage, /bandwidth sayfasını sunar.
func (sv *Server) bandwidthPage(w http.ResponseWriter, r *http.Request) {
	d := sv.collectBandwidth()

	if r.Header.Get("Accept") == "application/json" || r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, map[string]any{
			"total_kb":     d.TotalKB,
			"total_gb":     d.TotalGB,
			"latest_kbps":  d.LatestKbps,
			"limit_gb":     d.LimitGB,
			"used_percent": d.UsedPercent,
			"fetched_at":   d.FetchedAt,
			"project":      d.Project,
			"service":      d.Service,
			"source":       d.Source,
		})
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(renderBandwidthHTML(d)))
}

// renderBandwidthHTML, egress dashboard HTML'i.
func renderBandwidthHTML(d bandwidthData) string {
	color := "#30d158"
	if d.UsedPercent > 70 {
		color = "#ffd60a"
	}
	if d.UsedPercent > 90 {
		color = "#ff453a"
	}
	bar := d.UsedPercent
	if bar > 100 {
		bar = 100
	}

	errHTML := ""
	if d.VolErr != "" {
		errHTML += fmt.Sprintf(`<div class="err">⚠ Hacim hatası: %s</div>`, d.VolErr)
	}
	if d.EgrErr != "" {
		errHTML += fmt.Sprintf(`<div class="err">⚠ Hız hatası: %s</div>`, d.EgrErr)
	}
	// Kaynak bilgisi: Toplam Cloudflare Analytics'ten gelir (gerçek trafik),
	// anlık hız Northflank'tan (5dk gecikmeli). Kullanıcı veri kaynağını bilsin.
	if d.Source != "" {
		label := "Cloudflare Analytics"
		if d.Source == "northflank" {
			label = "Northflank"
		}
		errHTML += fmt.Sprintf(`<div class="hint">Veri kaynağı: %s · Toplam: son 30 gün · Anlık hız: son ölçüm</div>`, label)
	}

	return `<!DOCTYPE html>
<html lang="tr"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Hyaena Storage — Egress</title>
<link rel="icon" href="/static/favicon.svg" type="image/svg+xml">
<style>
:root{--bg:#0a0a0f;--card:rgba(255,255,255,.045);--border:rgba(255,255,255,.09);--text:#f5f5f7;--dim:#9b9ba7;--danger:#ff453a}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;background:var(--bg);color:var(--text);display:flex;justify-content:center;padding:60px 20px;min-height:100vh}
.card{background:var(--card);border:1px solid var(--border);border-radius:20px;padding:40px;max-width:560px;width:100%}
h1{font-size:20px;margin-bottom:6px;font-weight:700}
.sub{color:var(--dim);font-size:13px;margin-bottom:30px}
.stat{display:flex;justify-content:space-between;align-items:baseline;margin-bottom:10px}
.stat .lbl{color:var(--dim);font-size:13px}
.stat .val{font-size:28px;font-weight:800}
.bar{height:14px;background:rgba(255,255,255,.08);border-radius:99px;overflow:hidden;margin:16px 0 8px}
.fill{height:100%;border-radius:99px;transition:width .5s}
.hint{color:var(--dim);font-size:12px;margin-bottom:24px}
.foot{color:var(--dim);font-size:11px;margin-top:24px;border-top:1px solid var(--border);padding-top:14px}
.err{color:var(--danger);font-size:12px;margin-top:8px}
</style></head><body>
<div class="card">
<h1>📊 Egress Kullanımı</h1>
<div class="sub">Northflank egress — Cloudflare bypass sonrası</div>
<div class="stat"><span class="lbl">Toplam kullanım</span><span class="val">` + fmt.Sprintf("%.2f", d.TotalGB) + ` GB</span></div>
<div class="bar"><div class="fill" style="width:` + fmt.Sprintf("%.0f", bar) + `%;background:` + color + `"></div></div>
<div class="hint">` + fmt.Sprintf("%.2f / %.0f GB", d.TotalGB, d.LimitGB) + ` (%` + fmt.Sprintf("%.1f", d.UsedPercent) + `)</div>
<div class="stat"><span class="lbl">Anlık egress hızı</span><span class="val">` + fmt.Sprintf("%.1f", d.LatestKbps) + ` kbps</span></div>
` + errHTML + `
<div class="foot">Son güncelleme: ` + d.FetchedAt + ` UTC · proje: ` + d.Project + ` / servis: ` + d.Service + `</div>
</div></body></html>`
}
