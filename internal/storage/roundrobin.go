package storage

import (
	"database/sql"
	"errors"
	"strconv"
)

// metaKeyNextChannel, parça dağıtım sayaçının key'i.
const metaKeyNextChannel = "next_channel_index"

// getMetaInt, meta tablosundan sayısal değeri okur (yoksa 0).
func (s *Store) getMetaInt(key string) (int64, error) {
	var v string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key=$1", key).Scan(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// setMetaInt, meta tablosuna sayısal değeri yazar.
func (s *Store) setMetaInt(key string, n int64) error {
	_, err := s.db.Exec(
		"INSERT INTO meta (key, value) VALUES ($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value",
		key, strconv.FormatInt(n, 10),
	)
	return err
}

// NextChannelIndex, parça dağıtım sayaçını döndürür.
func (s *Store) NextChannelIndex() (int64, error) {
	return s.getMetaInt(metaKeyNextChannel)
}

// SetNextChannelIndex, parça dağıtım sayaçını ayarlar.
func (s *Store) SetNextChannelIndex(n int64) error {
	return s.setMetaInt(metaKeyNextChannel, n)
}

// PickUploadChannelsBalanced, parçaları kanallara SIRALI ve EŞİT dağıtır.
//
// Kalıcı sayaçtan başlayarak her parça bir sonraki kanala gider (wrap ile).
// Okuma + sayaç artırma tek transaction'da, satır kilidi (FOR UPDATE) ile atomik.
func (s *Store) PickUploadChannelsBalanced(channelIDs []int64, partCount int) ([]int64, error) {
	n := len(channelIDs)
	if n == 0 {
		return nil, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var start int64
	var v string
	err = tx.QueryRow(
		"SELECT value FROM meta WHERE key=$1 FOR UPDATE", metaKeyNextChannel,
	).Scan(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			start = 0
		} else {
			return nil, err
		}
	} else {
		start, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, err
		}
	}

	base := int(start % int64(n))
	plan := make([]int64, partCount)
	for i := 0; i < partCount; i++ {
		plan[i] = channelIDs[(base+i)%n]
	}

	// sayacı partCount kadar ilerlet (bir sonraki dosya kaldığı yerden devam etsin)
	_, err = tx.Exec(
		"INSERT INTO meta (key, value) VALUES ($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value",
		metaKeyNextChannel, strconv.FormatInt(start+int64(partCount), 10),
	)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return plan, nil
}
