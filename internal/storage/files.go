package storage

import "database/sql"

const fileCols = "id, token, original_name, size, mime, part_count, done_parts, status, error, created_at, ready_at, expires_at"

func scanFile(row interface{ Scan(...any) error }) (*File, error) {
	var f File
	var size, partCount, doneParts int64
	var errStr, expiresStr sql.NullString
	var createdAt, readyAt sql.NullTime
	err := row.Scan(&f.ID, &f.Token, &f.OriginalName, &size, &f.Mime,
		&partCount, &doneParts, &f.Status, &errStr, &createdAt, &readyAt, &expiresStr)
	if err != nil {
		return nil, err
	}
	f.Size = size
	f.PartCount = int(partCount)
	f.DoneParts = int(doneParts)
	f.CreatedAt = fmtTSValue(createdAt)
	if errStr.Valid {
		f.Error = &errStr.String
	}
	f.ReadyAt = fmtTS(readyAt)
	if expiresStr.Valid {
		f.ExpiresAt = &expiresStr.String
	}
	return &f, nil
}

// CreateFile, dosya kaydı ekler ve ID döndürür.
// expiresAt: süreli dosyalarda "YYYY-MM-DD HH:MM:SS"; "" ise süresiz (NULL).
func (s *Store) CreateFile(token, name string, size int64, mime string, partCount int, expiresAt string) (int64, error) {
	var exp any
	if expiresAt != "" {
		exp = expiresAt
	}
	var id int64
	err := s.db.QueryRow(
		"INSERT INTO files (token, original_name, size, mime, part_count, expires_at) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id",
		token, name, size, mime, partCount, exp,
	).Scan(&id)
	return id, err
}

// GetFileByToken, benzersiz token ile dosya döndürür.
func (s *Store) GetFileByToken(token string) (*File, error) {
	f, err := scanFile(s.db.QueryRow("SELECT "+fileCols+" FROM files WHERE token=$1", token))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return f, err
}

// GetFileByID, birincil anahtar ile dosya döndürür.
func (s *Store) GetFileByID(id int64) (*File, error) {
	f, err := scanFile(s.db.QueryRow("SELECT "+fileCols+" FROM files WHERE id=$1", id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return f, err
}

// UpdateFileSize, dosyanın boyutunu günceller (by-url akışı için).
func (s *Store) UpdateFileSize(id int64, size int64) error {
	_, err := s.db.Exec("UPDATE files SET size=$1 WHERE id=$2", size, id)
	return err
}

// UpdateFileMime, dosyanın MIME türünü günceller (by-url akışı için).
func (s *Store) UpdateFileMime(id int64, mime string) error {
	_, err := s.db.Exec("UPDATE files SET mime=$1 WHERE id=$2", mime, id)
	return err
}

// UpdateFilePartCount, dosyanın parça sayısını günceller ve eksik parçaları oluşturur.
func (s *Store) UpdateFilePartCount(id int64, partCount int) error {
	_, err := s.db.Exec("UPDATE files SET part_count=$1 WHERE id=$2", partCount, id)
	if err != nil {
		return err
	}
	return s.CreatePendingParts(id, partCount)
}

// UpdateFileStatus, durumu günceller. ready ise done_parts=part_count ve ready_at set edilir.
func (s *Store) UpdateFileStatus(id int64, status string, errMsg *string) error {
	if status == "ready" {
		_, err := s.db.Exec(
			"UPDATE files SET status=$1, done_parts=part_count, ready_at=now(), error=NULL WHERE id=$2",
			status, id,
		)
		return err
	}
	_, err := s.db.Exec("UPDATE files SET status=$1, error=$2 WHERE id=$3", status, errMsg, id)
	return err
}

// SetFileFailed, dosyayı failed durumuna alır.
func (s *Store) SetFileFailed(id int64, errMsg string) error {
	return s.UpdateFileStatus(id, "failed", &errMsg)
}

// BumpDoneParts, tamamlanan parça sayısını bir artırır.
func (s *Store) BumpDoneParts(id int64) error {
	_, err := s.db.Exec("UPDATE files SET done_parts = done_parts + 1 WHERE id=$1", id)
	return err
}

// ListFiles, limit/offset ile dosyaları yeni→eski sırada döndürür.
func (s *Store) ListFiles(limit, offset int) ([]File, error) {
	rows, err := s.db.Query("SELECT "+fileCols+" FROM files ORDER BY id DESC LIMIT $1 OFFSET $2", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// CountFiles, toplam dosya sayısı.
func (s *Store) CountFiles() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM files").Scan(&n)
	return n, err
}

// StorageUsage, hazır dosyaların toplam boyutu.
func (s *Store) StorageUsage() (int64, error) {
	var n int64
	err := s.db.QueryRow(
		"SELECT COALESCE(SUM(size),0) FROM files WHERE status='ready'",
	).Scan(&n)
	return n, err
}

// CountFilesToday, bugün oluşturulan dosya sayısı.
// SQLite localtime davranışına eşit: sunucu TZ'sine göre günlük dönüm.
func (s *Store) CountFilesToday() (int, error) {
	var n int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM files WHERE to_char(created_at AT TIME ZONE 'UTC' AT TIME ZONE 'Europe/Moscow', 'YYYY-MM-DD') = to_char(now() AT TIME ZONE 'Europe/Moscow', 'YYYY-MM-DD')",
	).Scan(&n)
	return n, err
}

// DeleteFile, dosyayı siler (parts cascade ile silinir).
func (s *Store) DeleteFile(id int64) error {
	_, err := s.db.Exec("DELETE FROM files WHERE id=$1", id)
	return err
}

// ListExpiredFiles, expires_at geçmiş ve hâlâ kayıtlı HAZIR dosyaları döndürür.
func (s *Store) ListExpiredFiles(now string) ([]File, error) {
	rows, err := s.db.Query(
		"SELECT "+fileCols+" FROM files WHERE status='ready' AND expires_at IS NOT NULL AND expires_at <= $1",
		now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}
