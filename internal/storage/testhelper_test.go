package storage

import (
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// onceSchema, test şemasını tüm test paketi için bir kez kurar (paralel testlerde deadlock önler).
var onceSchema sync.Once
var onceSchemaErr error

// sqlOpen, raw pgx bağlantısı açar (şema kurulumu için).
func sqlOpen(url string) (*sql.DB, error) {
	return sql.Open("pgx", url)
}

// testDBURL, Supabase DSN'sine izole test şeması ekler.
// DSN bulunamazsa "" döner (çağıran skip eder).
func testDBURL() string {
	base := os.Getenv("HYAENA_TEST_DB_URL")
	if base == "" {
		if data, err := os.ReadFile("../../.env"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "DATABASE_URL=") {
					base = strings.TrimSpace(strings.TrimPrefix(line, "DATABASE_URL="))
					break
				}
			}
		}
	}
	if base == "" {
		return ""
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	// simple_protocol: prepared statement cache çakışmasını önler (test izolasyonu)
	return base + sep + "search_path=tgshare_test&default_query_exec_mode=simple_protocol"
}

// newTestStore, izole test şemasına bağlanır ve her test öncesi temizler.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	url := testDBURL()
	if url == "" {
		t.Skip("DATABASE_URL bulunamadı — test atlandı")
	}
	// şemayı önce kur (paket başına bir kez — paralel testlerde deadlock önler)
	onceSchema.Do(func() {
		onceSchemaErr = ensureTestSchema(url)
	})
	if onceSchemaErr != nil {
		t.Fatalf("test şeması kurulamadı: %v", onceSchemaErr)
	}
	s, err := Open(url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	// test şemasındaki tabloları temizle (testler arası izolasyon)
	_, _ = s.db.Exec("TRUNCATE channels, files, parts, meta RESTART IDENTITY CASCADE")
	return s
}

// ensureTestSchema, tgshare_test şemasını oluşturur (paket başına bir kez).
func ensureTestSchema(url string) error {
	base := strings.SplitN(url, "?", 2)[0]
	// search_path'siz bağlan, şemayı kur
	db, err := sqlOpen(base)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec("CREATE SCHEMA IF NOT EXISTS tgshare_test")
	return err
}
