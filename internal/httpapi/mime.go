package httpapi

import "strings"

// allowedMimes, yükleyici/bizim onayladığımız içerik tipleri (F-01).
// image/svg+xml BİLİNÇLİ olarak yok: SVG top-level navigasyonda betik
// çalıştırır — inline servis edilmesi stored XSS olur.
var allowedMimes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true,
	"image/webp": true, "image/bmp": true,
	"video/mp4": true, "video/webm": true, "video/ogg": true,
	"audio/mpeg": true, "audio/ogg": true, "audio/wav": true, "audio/webm": true,
	"application/pdf": true, "application/zip": true,
	"text/plain":               true,
	"application/octet-stream": true,
}

// sanitizeMime, istemci/uzak sunucu MIME değerini güvenli forma indirger:
// parametre satırı atılır (;charset=...), küçük harfe çevrilir ve allowlist
// dışındaki HER değer application/octet-stream olur (F-01).
func sanitizeMime(m string) string {
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = m[:i]
	}
	m = strings.ToLower(strings.TrimSpace(m))
	if len(m) > 200 {
		return "application/octet-stream"
	}
	if !allowedMimes[m] {
		return "application/octet-stream"
	}
	return m
}
