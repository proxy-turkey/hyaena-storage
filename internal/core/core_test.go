package core

import (
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
		"../../etc/passwd":    "etc_passwd",
		"":                    "dosya",
		"hoş dosya.bin":       "hoş dosya.bin",
		"...":                 "dosya",
		"rapor (final).pdf":   "rapor (final).pdf",
	}
	for src, want := range cases {
		if got := SanitizeFilename(src); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", src, got, want)
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
