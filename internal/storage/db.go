// Package storage, Supabase (Postgres) veritabanı erişimi sağlar.
// SQLite sürümünün yerini alır; aynı Store arayüzünü sunar.
package storage

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// schema, Postgres DDL — SQLite şemasının birebir karşılığı.
// Otomatik kurulur (CREATE IF NOT EXISTS), idempotent.
const schema = `
CREATE TABLE IF NOT EXISTS channels (
    id          BIGSERIAL PRIMARY KEY,
    telegram_id BIGINT NOT NULL,
    access_hash BIGINT,
    title       TEXT NOT NULL,
    created_day TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_channels_day ON channels(created_day);

CREATE TABLE IF NOT EXISTS files (
    id            BIGSERIAL PRIMARY KEY,
    token         TEXT NOT NULL UNIQUE,
    original_name TEXT NOT NULL,
    size          BIGINT NOT NULL,
    mime          TEXT NOT NULL DEFAULT 'application/octet-stream',
    part_count    INTEGER NOT NULL,
    done_parts    INTEGER NOT NULL DEFAULT 0,
    status        TEXT NOT NULL DEFAULT 'uploading',
    error         TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    ready_at      TIMESTAMPTZ,
    expires_at    TEXT
);
CREATE INDEX IF NOT EXISTS idx_files_status ON files(status);
CREATE INDEX IF NOT EXISTS idx_files_expires ON files(expires_at);

CREATE TABLE IF NOT EXISTS parts (
    id              BIGSERIAL PRIMARY KEY,
    file_id         BIGINT NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    part_index      INTEGER NOT NULL,
    channel_id      BIGINT REFERENCES channels(id) ON DELETE SET NULL,
    telegram_msg_id INTEGER,
    file_id_blob    BYTEA,
    size            BIGINT NOT NULL,
    dc_id           INTEGER,
    status          TEXT NOT NULL DEFAULT 'pending',
    error           TEXT,
    uploaded_at     TIMESTAMPTZ,
    UNIQUE(file_id, part_index)
);
CREATE INDEX IF NOT EXISTS idx_parts_file ON parts(file_id);

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

// Store, Postgres bağlantısı + tüm CRUD.
type Store struct {
	db *sql.DB
}

// Open, Supabase Postgres'e bağlanır ve şemayı otomatik kurar.
func Open(url string) (*Store, error) {
	// simple_protocol: pgx prepared-statement cache çakışmalarını önler
	// (şema yeniden kurulduğunda veya birden çok bağlantıda "already exists")
	if !strings.Contains(url, "default_query_exec_mode=") {
		sep := "?"
		if strings.Contains(url, "?") {
			sep = "&"
		}
		url += sep + "default_query_exec_mode=simple_protocol"
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("postgres bağlantı hatası: %w", err)
	}
	// eşzamanlılık: sayaç transaction'ında FOR UPDATE ile güvence; pool serbest
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres ping hatası: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("şema oluşturulamadı: %w", err)
	}
	return &Store{db: db}, nil
}

// Close, bağlantıyı kapatır.
func (s *Store) Close() error { return s.db.Close() }

// fmtTS, TIMESTAMPTZ'i struct'ların beklediği UTC string formatına çevirir.
func fmtTS(t sql.NullTime) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.UTC().Format("2006-01-02 15:04:05")
	return &s
}

// fmtTSValue, TIMESTAMPTZ'i UTC string olarak döndürür (NULL olabilir).
func fmtTSValue(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format("2006-01-02 15:04:05")
}
