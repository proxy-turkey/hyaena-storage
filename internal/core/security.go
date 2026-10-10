package core

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// shareTokenRe, paylaşım token'ı doğrulaması (Python TOKEN_RE birebir).
var shareTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{20,64}$`)

// unsafeChars, sanitize_filename'de "_" ile değiştirilen karakterler.
// Python'da kontrol karakterleri + Windows yasaklı karakterler; agresif modda
// tüm boşluklar (\s) ve URL parça ayırıcısı # da dahil (linki kırarlar).
var unsafeChars = regexp.MustCompile(`[:*?"<>|\x00-\x1f\s#]`)

// latinReplacer, aksanlı/özel Latin karakterleri ASCII karşılıklarına çevirir.
// Sanitize agresif modda (NFKD + birleşik işaret silme sonrası) kalan harfler.
var latinReplacer = strings.NewReplacer(
	"ı", "i", "İ", "I", "ß", "ss", "ø", "o", "Ø", "O",
	"ð", "d", "Ð", "D", "ł", "l", "Ł", "L", "đ", "d",
	"Đ", "D", "œ", "oe", "Œ", "OE", "æ", "ae", "Æ", "AE",
	"þ", "th", "Þ", "TH", "ſ", "s",
)

// asciiFold, ismi ASCII'ye indirger: NFKD → birleşik işaretleri sil → harf haritala.
func asciiFold(name string) string {
	t := transform.Chain(norm.NFKD, runes.Remove(runes.In(unicode.Mn)))
	res, _, err := transform.String(t, name)
	if err != nil {
		res = name
	}
	return latinReplacer.Replace(res)
}

// MakeShareToken, tahmin edilemez paylaşım token'ı üretir (~130 bit, 22 karakter).
func MakeShareToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ValidShareToken, token'ın geçerli formatta olup olmadığını döndürür.
func ValidShareToken(token string) bool {
	return shareTokenRe.MatchString(token)
}

// adminBucket, now'ı TTL saatine göre dilimler (Python _ts_bucket).
func adminBucket(now time.Time, ttlHours int) int64 {
	return now.Unix() / int64(ttlHours*3600)
}

// MakeAdminToken, oturum-başı nonce'lu HMAC-SHA256 admin token üretir:
// b64url(sid) + "." + b64url(HMAC(secret, bucket || sid)). Aynı haftadaki
// iki giriş artık BİREBİR AYNI çerez üretmez (F-06) ve sid, sunucu
// tarafında iptal edilebilir tek oturum kimliğidir.
func MakeAdminToken(secret []byte, ttlHours int, now time.Time) string {
	sid := make([]byte, 16)
	if _, err := rand.Read(sid); err != nil {
		panic("crypto/rand başarısız: " + err.Error())
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(fmt.Sprintf("%d", adminBucket(now, ttlHours))))
	mac.Write(sid)
	return base64.RawURLEncoding.EncodeToString(sid) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// AdminTokenSID, token'dan oturum kimliğini (sid) çıkarır — iptal
// listesinde arama için. Hatalı biçim boş döner.
func AdminTokenSID(token string) string {
	i := strings.IndexByte(token, '.')
	if i <= 0 {
		return ""
	}
	return token[:i]
}

// VerifyAdminToken, token'ı sabit-zamanlı doğrular: sid ayrıştırılır,
// güncel kova üzerinden MAC yeniden hesaplanır (nonce kovaya bağlıdır).
func VerifyAdminToken(secret []byte, ttlHours int, token string, now time.Time) bool {
	if token == "" {
		return false
	}
	i := strings.IndexByte(token, '.')
	if i <= 0 || i+1 >= len(token) {
		return false
	}
	sid, err := base64.RawURLEncoding.DecodeString(token[:i])
	if err != nil {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(token[i+1:])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(fmt.Sprintf("%d", adminBucket(now, ttlHours))))
	mac.Write(sid)
	return hmac.Equal(got, mac.Sum(nil))
}

// SanitizeFilename, dosya adını güvenli ve evrensel uyumlu hale getirir.
//
// Agresif mod: NFKD normalize → birleşik işaretler (aksan) silinir → Latin
// özel harfler ASCII'ye çevrilir → boşluklar dahil tüm güvensiz karakterler
// "_" yapılır. Amaç: linklerde/header'larda/Telegram'da her ortamda çalışan
// garanti ad (kullanıcı seçimi — 2026-08-12).
//
//	"../../etc/passwd" → "etc_passwd",  "" → "dosya",  "hoş dosya.bin" → "hos_dosya.bin"
func SanitizeFilename(name string) string {
	if name == "" {
		return "dosya"
	}
	// ASCII'ye indirge (NFKD + aksan silme + harf haritalama)
	name = asciiFold(name)
	// yol ayırıcılarına göre segmentlere ayır, . ve .. segmentlerini düş
	var segs []string
	for _, s := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if s != "" && s != "." && s != ".." {
			segs = append(segs, s)
		}
	}
	name = strings.Join(segs, "_")
	// kalan tehlikeli + kontrol + boşluk karakterleri "_" yap
	name = unsafeChars.ReplaceAllString(name, "_")
	// ardışık "_"leri tek "_"ye daralt (boşluklar "_" olduğu için sık görülür)
	name = collapseUnderscores(name)
	name = strings.Trim(name, " ._")
	if name == "" {
		return "dosya"
	}
	// max 255 rune + 255 byte (UTF-8 güvenli)
	name = truncateBytes(name, 255)
	if name == "" {
		return "dosya"
	}
	return name
}

// collapseUnderscores, ardışık "_" karakterlerini tek "_"ye daraltır.
func collapseUnderscores(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prev := false
	for _, r := range s {
		if r == '_' {
			if prev {
				continue
			}
			prev = true
		} else {
			prev = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

func truncateBytes(s string, max int) string {
	for len(s) > max {
		rs := []rune(s)
		s = string(rs[:len(rs)-1])
	}
	return s
}
