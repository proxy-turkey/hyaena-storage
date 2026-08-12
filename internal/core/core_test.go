package core

import (
	"strings"
	"testing"
	"time"
)

func TestPartCount(t *testing.T) {
	seg := int64(20 * 1024 * 1024)
	cases := []struct {
		size int64
		want int
	}{
		{seg * 4, 4},          // exact multiple
		{seg*4 + 1, 5},        // remainder
		{1000, 1},             // small file
		{0, 1},                // empty
		{seg*4 - 1, 4},        // just under
	}
	for _, c := range cases {
		if got := PartCount(c.size, seg); got != c.want {
			t.Errorf("PartCount(%d, %d) = %d, want %d", c.size, seg, got, c.want)
		}
	}
}

func TestAdminToken(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now()
	tok := MakeAdminToken(secret, 168, now)
	if !VerifyAdminToken(secret, 168, tok, now) {
		t.Error("doğru token kabul edilmedi")
	}
	if VerifyAdminToken(secret, 168, "yanlis", now) {
		t.Error("yanlış token kabul edildi")
	}
}

func TestAdminTokenTTL(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now()
	// Token bir TTL önce üretildiyse mevcut dilimde geçersiz olmalı
	past := now.Add(-(168*3600 + 60) * time.Second)
	pastTok := MakeAdminToken(secret, 168, past)
	if VerifyAdminToken(secret, 168, pastTok, now) {
		t.Error("eski dilimdeki token kabul edildi")
	}
}

func TestShareToken(t *testing.T) {
	for i := 0; i < 20; i++ {
		tok := MakeShareToken()
		if !ValidShareToken(tok) {
			t.Errorf("token geçersiz: %q", tok)
		}
		if len(tok) < 20 {
			t.Errorf("token çok kısa: %q", tok)
		}
	}
	if ValidShareToken("../../etc/passwd") {
		t.Error("path traversal token kabul edildi")
	}
	if ValidShareToken("kisa") {
		t.Error("kısa token kabul edildi")
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":     "etc_passwd",
		"":                     "dosya",
		"hoş dosya.bin":        "hos_dosya.bin",
		"...":                  "dosya",
		"rapor (final).pdf":    "rapor_(final).pdf",
		"Soru?nasil.pdf":       "Soru_nasil.pdf",
		"a/b.txt":              "a_b.txt",
		"a\\b.txt":             "a_b.txt",
		"#rapor.pdf":           "rapor.pdf",
		"a  b c.txt":           "a_b_c.txt",
		"şekerli çay.png":      "sekerli_cay.png",
		"ÜNİVERSİTE.pdf":       "UNIVERSITE.pdf",
		"data:image.txt":       "data_image.txt",
		"..gizli.txt":          "gizli.txt",
		"name*with?pipe|.bin":  "name_with_pipe_.bin",
		"tab\tseparated.txt":   "tab_separated.txt",
		"café résumé.docx":     "cafe_resume.docx",
		"straße.txt":           "strasse.txt",
	}
	for src, want := range cases {
		if got := SanitizeFilename(src); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", src, got, want)
		}
	}
}

func TestSanitizeFilenameLong(t *testing.T) {
	// 300 karakterlik isim 255'e iner ve geçerli bir ad olarak kalır
	long := strings.Repeat("ş", 300) + ".pdf"
	got := SanitizeFilename(long)
	if len(got) > 255 {
		t.Errorf("uzun isim 255'i aştı: %d", len(got))
	}
	if got == "" {
		t.Error("uzun isim boş dönmemeli")
	}
}

func TestSanitizeFilenameOnlyUnsafe(t *testing.T) {
	// sadece güvensiz karakterler → yedek ad
	for _, src := range []string{"???", "***", "   ", "\t\n", "###", "   ...   "} {
		if got := SanitizeFilename(src); got != "dosya" {
			t.Errorf("SanitizeFilename(%q) = %q, want dosya", src, got)
		}
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(2, 60)
	if !rl.Check("ip1") {
		t.Error("1. istek reddedildi")
	}
	if !rl.Check("ip1") {
		t.Error("2. istek reddedildi")
	}
	if rl.Check("ip1") {
		t.Error("3. istek kabul edildi (limit 2)")
	}
	if !rl.Check("ip2") {
		t.Error("farklı ip bağımsız olmalı")
	}
}
