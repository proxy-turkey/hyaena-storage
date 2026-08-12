package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
}

func (sv *Server) collectBandwidth() bandwidthData {
	d := bandwidthData{LimitGB: 10, FetchedAt: time.Now().UTC().Format("2006-01-02 15:04:05"), Project: sv.s.NFProject, Service: sv.s.NFService}

	// Toplam kullanım: bandwidthVolume (kb). Cloudflare bypass aktifken bu ~0
	// olur (origin'e egress gitmiyor) — bu beklenen ve istenen davranış.
	vol, volErr := sv.northflankMetrics("bandwidthVolume")
	if volErr == nil {
		if md, ok := vol["bandwidthVolume"]; ok {
			for _, c := range md.Values {
				for _, p := range c.Data {
					d.TotalKB += p.Value
				}
			}
		}
	} else {
		d.VolErr = volErr.Error()
	}

	// Anlık egress hızı: networkEgress (kbps). Tüm container'ların EN SON
	// değerlerinin toplamı (önceki kod sadece son container'ın son değerini
	// alıyordu — diğer container'ların hızı yok sayılıyordu).
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
	// Cloudflare bypass aktifken toplam ~0 kalır — kullanıcı bunu "çalışmıyor"
	// sanmasın. İndirilen trafik Cloudflare edge cache'inden servis edilir.
	if d.TotalGB < 0.001 && d.LatestKbps > 0 {
		errHTML += `<div class="err" style="color:var(--dim)">ℹ Egress bypass aktif — indirilen trafik Cloudflare edge cache'inden servis ediliyor, Northflank origin'e neredeyse hiç egress gitmiyor. Toplam bu yüzden ~0.</div>`
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
