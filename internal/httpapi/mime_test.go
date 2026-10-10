package httpapi

import "testing"

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
