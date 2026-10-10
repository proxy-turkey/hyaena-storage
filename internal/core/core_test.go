package core

import (
	"net/http"
	"net/http/httptest"
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
		{seg * 4, 4},   // exact multiple
		{seg*4 + 1, 5}, // remainder
		{1000, 1},      // small file
		{0, 1},         // empty
		{seg*4 - 1, 4}, // just under
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
		"hoş dosya.bin":       "hos_dosya.bin",
		"...":                 "dosya",
		"rapor (final).pdf":   "rapor_(final).pdf",
		"Soru?nasil.pdf":      "Soru_nasil.pdf",
		"a/b.txt":             "a_b.txt",
		"a\\b.txt":            "a_b.txt",
		"#rapor.pdf":          "rapor.pdf",
		"a  b c.txt":          "a_b_c.txt",
		"şekerli çay.png":     "sekerli_cay.png",
		"ÜNİVERSİTE.pdf":      "UNIVERSITE.pdf",
		"data:image.txt":      "data_image.txt",
		"..gizli.txt":         "gizli.txt",
		"name*with?pipe|.bin": "name_with_pipe_.bin",
		"tab\tseparated.txt":  "tab_separated.txt",
		"café résumé.docx":    "cafe_resume.docx",
		"straße.txt":          "strasse.txt",
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

func TestRateLimiterPrunesStaleKeys(t *testing.T) {
	rl := NewRateLimiter(1, 1) // 1 istek / 1 sn
	rl.Check("eski-ip")
	if len(rl.hits) != 1 {
		t.Fatalf("başlangıç: 1 anahtar beklenirdi, %d", len(rl.hits))
	}
	time.Sleep(1100 * time.Millisecond)
	rl.Check("yeni-ip") // prune tetiklenmeli (pencere geçti)
	if _, ok := rl.hits["eski-ip"]; ok {
		t.Error("pencere dışı anahtar budanmamış — bellek sızıntısı")
	}
}

func TestClientIPTrustedProxyOnly(t *testing.T) {
	mk := func(remote, cip, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if cip != "" {
			r.Header.Set("CF-Connecting-IP", cip)
		}
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}

	// CF ucundan geliyorsa: CF-Connecting-IP geçerli, spoof edilemez.
	if got := ClientIP(mk("104.16.1.1:443", "1.2.3.4", "9.9.9.9")); got != "1.2.3.4" {
		t.Errorf("CF ucundan CF-Connecting-IP = %q, want 1.2.3.4", got)
	}
	// CF ucundan, CF-Connecting-IP yoksa XFF ilk değeri.
	if got := ClientIP(mk("172.64.0.5:80", "", "5.6.7.8, 10.0.0.1")); got != "5.6.7.8" {
		t.Errorf("CF ucundan XFF = %q, want 5.6.7.8", got)
	}
	// Doğrudan bağlantı: başlıklar YOK SAYILIR, RemoteAddr esas alınır
	// (aksi halde XFF sahtelemekle rate limit atlanırdı).
	if got := ClientIP(mk("203.0.113.10:52100", "1.2.3.4", "9.9.9.9")); got != "203.0.113.10" {
		t.Errorf("doğrudan bağlantı = %q, want RemoteAddr (203.0.113.10)", got)
	}
}

func TestAdminTokenNonceUniqueness(t *testing.T) {
	secret := []byte("s")
	now := time.Now()
	a := MakeAdminToken(secret, 24, now)
	b := MakeAdminToken(secret, 24, now)
	if a == b {
		t.Fatal("aynı kovadaki iki giriş birebir aynı token üretti (nonce yok)")
	}
	if !VerifyAdminToken(secret, 24, a, now) || !VerifyAdminToken(secret, 24, b, now) {
		t.Fatal("nonce'lu tokenlar doğrulanamadı")
	}
	tampered := "AAAA." + a[strings.Index(a, ".")+1:]
	if VerifyAdminToken(secret, 24, tampered, now) {
		t.Fatal("taklit sid kabul edildi")
	}
	if AdminTokenSID(a) == "" || strings.Contains(AdminTokenSID(a), ".") {
		t.Fatal("AdminTokenSID hatalı")
	}
}

func TestNormalizeIPKeyIPv6Slash64(t *testing.T) {
	a := NormalizeIPKey("2001:db8:aaaa:bbbb::1")
	b := NormalizeIPKey("2001:db8:aaaa:bbbb::ffff")
	if a != b {
		t.Fatalf("aynı /64 farklı anahtar: %q vs %q", a, b)
	}
	if got := NormalizeIPKey("192.0.2.1"); got != "192.0.2.1" {
		t.Fatalf("IPv4 kanonik değil: %q", got)
	}
	// rotasyonlu CF-Connecting-IP → tek kova (F-11 regresyonu)
	r1 := httptest.NewRequest("GET", "/", nil)
	r1.RemoteAddr = "104.16.0.1:443"
	r1.Header.Set("CF-Connecting-IP", "2001:db8:aaaa:bbbb::1")
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "104.16.0.1:443"
	r2.Header.Set("CF-Connecting-IP", "2001:db8:aaaa:bbbb::ffff")
	rl := NewRateLimiter(1, 60)
	if !rl.Check(ClientIP(r1)) {
		t.Fatal("1. istek reddedildi")
	}
	if rl.Check(ClientIP(r2)) {
		t.Fatal("aynı /64'den 2. istek kabul edildi — rotasyon hâlâ bypass")
	}
}
