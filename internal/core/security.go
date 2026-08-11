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

	"golang.org/x/text/unicode/norm"
)

// shareTokenRe, paylaşım token'ı doğrulaması (Python TOKEN_RE birebir).
var shareTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{20,64}$`)

// unsafeChars, sanitize_filename'de "_" ile değiştirilen karakterler (Python birebir).
var unsafeChars = regexp.MustCompile(`[:*?"<>|\x00-\x1f]`)

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

// MakeAdminToken, HMAC-SHA256 zaman-dilimli admin token üretir (Python birebir).
func MakeAdminToken(secret []byte, ttlHours int, now time.Time) string {
	bucket := adminBucket(now, ttlHours)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(fmt.Sprintf("%d", bucket)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyAdminToken, token'ı sabit-zamanlı doğrular.
func VerifyAdminToken(secret []byte, ttlHours int, token string, now time.Time) bool {
	if token == "" {
		return false
	}
	expected := MakeAdminToken(secret, ttlHours, now)
	return hmac.Equal([]byte(token), []byte(expected))
}

// SanitizeFilename, dosya adını güvenli hale getirir (Python birebir).
//
//	"../../etc/passwd" → "etc_passwd",  "" → "dosya", "..." → "dosya"
func SanitizeFilename(name string) string {
	if name == "" {
		return "dosya"
	}
	// NFC normalize (Unicode)
	name = norm.NFC.String(name)
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
	// kalan tehlikeli + kontrol karakterleri "_" yap
	name = unsafeChars.ReplaceAllString(name, "_")
	name = strings.Trim(name, " .")
	if name == "" {
		return "dosya"
	}
	// max 255 byte (Python len[:255])
	runes := []rune(name)
	if len(runes) > 255 {
		runes = runes[:255]
		name = string(runes)
	}
	// byte sınırı da uygula (UTF-8 güvenli kesim)
	if len([]byte(name)) > 255 {
		name = truncateBytes(name, 255)
	}
	if name == "" {
		return "dosya"
	}
	return name
}

func truncateBytes(s string, max int) string {
	for len(s) > max {
		rs := []rune(s)
		s = string(rs[:len(rs)-1])
	}
	return s
}
