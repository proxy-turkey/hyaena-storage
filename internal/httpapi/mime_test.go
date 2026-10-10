package httpapi

import (
	"net"
	"testing"
)

// F-01: yükleyici MIME'ı allowlist dışında octet-stream'e düşer; SVG asla
// geçmez; inline listede SVG/text yok.
func TestSanitizeMime(t *testing.T) {
	cases := map[string]string{
		"image/svg+xml":             "application/octet-stream",
		"IMAGE/SVG+XML":             "application/octet-stream",
		"text/html":                 "application/octet-stream",
		"application/x-httpd-php":   "application/octet-stream",
		"image/png; charset=binary": "image/png",
		" Video/MP4 ":               "video/mp4",
		"":                          "application/octet-stream",
		"application/pdf":           "application/pdf",
	}
	for in, want := range cases {
		if got := sanitizeMime(in); got != want {
			t.Errorf("sanitizeMime(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsInlineTypeRejectsActiveContent(t *testing.T) {
	for _, m := range []string{"image/svg+xml", "text/html", "text/plain", "application/octet-stream", ""} {
		if isInlineType(m) {
			t.Errorf("isInlineType(%q) = true, aktif içerik inline olmamalı", m)
		}
	}
	for _, m := range []string{"image/png", "video/mp4", "audio/mpeg", "application/pdf"} {
		if !isInlineType(m) {
			t.Errorf("isInlineType(%q) = false, güvenli tip inline olmalı", m)
		}
	}
}

// C08 sertleştirme: CGNAT/benchmark/reserved aralıkları artık engellenir.
func TestIsBlockedIPCGNAT(t *testing.T) {
	blocked := []string{"100.64.0.1", "100.100.100.100", "100.127.255.254", "192.0.0.1", "198.18.0.5", "240.0.0.1"}
	for _, s := range blocked {
		if !isBlockedIP(net.ParseIP(s)) {
			t.Errorf("isBlockedIP(%s) = false, engellenmeli", s)
		}
	}
	allowed := []string{"1.1.1.1", "203.0.113.10", "93.184.216.34"}
	for _, s := range allowed {
		if isBlockedIP(net.ParseIP(s)) {
			t.Errorf("isBlockedIP(%s) = true, genel adres engellenmemeli", s)
		}
	}
}
