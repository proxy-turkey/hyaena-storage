// Package config yükler .env + config.ini ayarlarını.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Settings, uygulamanın tüm yapılandırmasını tutar (Python config.py karşılığı).
type Settings struct {
	TelegramAPIID   int
	TelegramAPIHash string
	TwoFA           string
	SessionFile     string

	AdminPassword   string
	AdminCookieName string
	AdminTokenSecret string
	TokenTTLHours   int

	SegmentBytes      int64
	MaxUploadBytes    int64
	DownloadChunkSize int64
	InterMessageSleep float64

	BootstrapChannels int
	ChannelIntervalSN float64
	ChannelCreationHour int

	Host string
	Port int
	RateLimitPerMin int

	// PublicBaseURL: paylaşım linklerinde kullanılacak dış taban URL.
	// Cloudflare cache'i büyük dosyaları kestiği için, download'lar doğrudan
	// orfi sunucusuna (direct.hyaena.co.uk:8080) gider. Boşsa request origin'i
	// kullanılır (eski davranış).
	PublicBaseURL string

	DatabaseURL string // Supabase/Postgres connection string
	TmpRoot     string

	// Cloudflare cache purge (dosya silinince Worker cache'ten düşsün)
	CFZoneID  string
	CFAPIKey  string
	CFAPIEmail string

	// Northflank metrics (egress izleme)
	NFToken     string
	NFProject   string
	NFService   string

	// türetilmiş
	ProjectRoot string
}

// Default settings varsayılanları.
func Defaults() *Settings {
	return &Settings{
		AdminCookieName:   "tgshare_admin",
		TokenTTLHours:     168,
		SegmentBytes:      20 * 1024 * 1024,
		MaxUploadBytes:    100 * 1024 * 1024 * 1024,
		DownloadChunkSize: 512 * 1024,
		InterMessageSleep: 1.0,
		BootstrapChannels: 1,
		ChannelIntervalSN: 90.0,
		ChannelCreationHour: 4,
		Host:              "0.0.0.0", // container (Northflank) için; yerel test .env'de HOST=127.0.0.1
		Port:              8000,
		RateLimitPerMin:   20,
		TmpRoot:           "data/tmp",
	}
}

// parseEnv dosya içeriğini KEY=VALUE satırlarına ayrıştırır (# yorum, çift tırnak, export).
func parseEnv(content string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		out[key] = val
	}
	return out
}

// loadEnvFile .env dosyasını okur (yoksa boş map).
func loadEnvFile(path string) map[string]string {
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	return parseEnv(string(b))
}

// parseINI config.ini'nin [section] key = value kısmını okur.
func parseINI(content string) map[string]string {
	out := map[string]string{}
	section := ""
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := section + "." + strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		out[key] = val
	}
	return out
}

// Load, .env + config.ini okur ve Settings döndürür. root: proje kökü.
func Load(root string) (*Settings, error) {
	s := Defaults()
	s.ProjectRoot = root

	env := loadEnvFile(filepath.Join(root, ".env"))
	ini := parseINI(readFile(filepath.Join(root, "config.ini")))

	// Öncelik: process env (Northflank), sonra .env dosyası, sonra config.ini, sonra default.
	// NOT: .env dosyası process env'i EZER (getStr env haritasını önce kontrol eder);
	// container'da .env dosyası yoksa process env kullanılır.
	getStr := func(envKey, iniKey, def string) string {
		if v := os.Getenv(envKey); v != "" {
			return v
		}
		if v, ok := env[envKey]; ok {
			return v
		}
		if v, ok := ini[iniKey]; ok {
			return v
		}
		return def
	}
	getInt := func(envKey string, def int) int {
		if v := os.Getenv(envKey); v != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n
			}
		}
		if v, ok := env[envKey]; ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n
			}
		}
		return def
	}
	getInt64 := func(envKey string, def int64) int64 {
		if v := os.Getenv(envKey); v != "" {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return n
			}
		}
		if v, ok := env[envKey]; ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return n
			}
		}
		return def
	}
	getFloat := func(envKey string, def float64) float64 {
		if v := os.Getenv(envKey); v != "" {
			if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return n
			}
		}
		if v, ok := env[envKey]; ok {
			if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return n
			}
		}
		return def
	}

	// config.ini (api_id/api_hash) için process env fallback — container'da dosya gerekmez
	apiID := getStr("TELEGRAM_API_ID", "telegram.api_id", "")
	apiHash := getStr("TELEGRAM_API_HASH", "telegram.api_hash", "")
	if apiID == "" || apiHash == "" {
		return nil, fmt.Errorf("TELEGRAM_API_ID/api_id ve TELEGRAM_API_HASH/api_hash gerekli")
	}
	id, err := strconv.Atoi(apiID)
	if err != nil {
		return nil, fmt.Errorf("api_id sayı olmalı: %w", err)
	}

	s.TelegramAPIID = id
	s.TelegramAPIHash = apiHash
	s.TwoFA = getStr("TELEGRAM_TWO_FA", "options.two_fa_password", "")
	s.SessionFile = getStr("SESSION_FILE", "", "session")

	s.AdminPassword = getStr("ADMIN_PASSWORD", "", "degistir-ben")
	s.AdminTokenSecret = getStr("ADMIN_TOKEN_SECRET", "", "")
	s.SegmentBytes = getInt64("SEGMENT_BYTES", s.SegmentBytes)
	s.MaxUploadBytes = getInt64("MAX_UPLOAD_BYTES", s.MaxUploadBytes)
	s.InterMessageSleep = getFloat("INTER_MESSAGE_SLEEP", s.InterMessageSleep)
	s.BootstrapChannels = getInt("BOOTSTRAP_CHANNELS", s.BootstrapChannels)
	s.ChannelIntervalSN = getFloat("CHANNEL_INTERVAL_SN", s.ChannelIntervalSN)
	s.Host = getStr("HOST", "", s.Host)
	s.Port = getInt("PORT", s.Port)
	s.RateLimitPerMin = getInt("RATE_LIMIT_PER_MIN", s.RateLimitPerMin)
	s.DatabaseURL = getStr("DATABASE_URL", "", "")
	s.TmpRoot = getStr("TMP_ROOT", "", s.TmpRoot)
	s.PublicBaseURL = getStr("PUBLIC_BASE_URL", "", "")
	s.CFZoneID = getStr("CF_ZONE_ID", "", "")
	s.CFAPIKey = getStr("CF_API_KEY", "", "")
	s.CFAPIEmail = getStr("CF_API_EMAIL", "", "")
	s.NFToken = getStr("NORTHFLANK_TOKEN", "", "")
	s.NFProject = getStr("NORTHFLANK_PROJECT", "hyaena", "")
	s.NFService = getStr("NORTHFLANK_SERVICE", "hyaena-storage", "")

	return s, nil
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// TmpDir, proje köküne göre geçici dizini döndürür.
func (s *Settings) TmpDir() string {
	if filepath.IsAbs(s.TmpRoot) {
		return s.TmpRoot
	}
	return filepath.Join(s.ProjectRoot, s.TmpRoot)
}

// DBFile, Postgres connection string döndürür (Supabase).
func (s *Settings) DBFile() string {
	return s.DatabaseURL
}

// TokenSecret, admin imza sırrını üretir: açıkça verilmemişse PBKDF2.
func (s *Settings) TokenSecret() []byte {
	if s.AdminTokenSecret != "" {
		return []byte(s.AdminTokenSecret)
	}
	return pbkdf2Key(s.AdminPassword)
}
