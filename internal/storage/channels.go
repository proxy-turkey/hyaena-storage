package storage

import "database/sql"

const channelCols = "id, telegram_id, access_hash, title, created_day, created_at"

// CountChannels, toplam kanal sayısını döndürür.
func (s *Store) CountChannels() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM channels").Scan(&n)
	return n, err
}

// ListChannels, tüm kanalları id sırasına göre döndürür.
func (s *Store) ListChannels() ([]Channel, error) {
	rows, err := s.db.Query("SELECT " + channelCols + " FROM channels ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		var accessHash sql.NullInt64
		var createdAt sql.NullTime
		if err := rows.Scan(&c.ID, &c.TelegramID, &accessHash, &c.Title, &c.CreatedDay, &createdAt); err != nil {
			return nil, err
		}
		c.AccessHash = accessHash.Int64
		c.CreatedAt = fmtTSValue(createdAt)
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChannel, id ile bir kanalı döndürür.
func (s *Store) GetChannel(id int64) (*Channel, error) {
	var c Channel
	var accessHash sql.NullInt64
	var createdAt sql.NullTime
	err := s.db.QueryRow(
		"SELECT "+channelCols+" FROM channels WHERE id=$1", id,
	).Scan(&c.ID, &c.TelegramID, &accessHash, &c.Title, &c.CreatedDay, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.AccessHash = accessHash.Int64
	c.CreatedAt = fmtTSValue(createdAt)
	return &c, nil
}

// CreateChannel, kanal kaydı ekler ve ID döndürür.
func (s *Store) CreateChannel(telegramID, accessHash int64, title, createdDay string) (int64, error) {
	var id int64
	err := s.db.QueryRow(
		"INSERT INTO channels (telegram_id, access_hash, title, created_day) VALUES ($1,$2,$3,$4) RETURNING id",
		telegramID, accessHash, title, createdDay,
	).Scan(&id)
	return id, err
}

// UpdateChannelTitle, kanalın başlığını günceller.
func (s *Store) UpdateChannelTitle(id int64, title string) error {
	_, err := s.db.Exec("UPDATE channels SET title=$1 WHERE id=$2", title, id)
	return err
}

// ChannelExistsForDay, verilen gün için kanal var mı (idempotent günlük kontrol).
func (s *Store) ChannelExistsForDay(day string) (bool, error) {
	var one int
	err := s.db.QueryRow(
		"SELECT 1 FROM channels WHERE created_day=$1 ORDER BY id LIMIT 1", day,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
