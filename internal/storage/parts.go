package storage

import "database/sql"

const partCols = "id, file_id, part_index, channel_id, telegram_msg_id, file_id_blob, size, dc_id, status, error, uploaded_at"

func scanPart(row interface{ Scan(...any) error }) (*Part, error) {
	var p Part
	var chID, msgID, dcID sql.NullInt64
	var blob []byte
	var size int64
	var errStr sql.NullString
	var uploadedAt sql.NullTime
	err := row.Scan(&p.ID, &p.FileID, &p.PartIndex, &chID, &msgID, &blob, &size,
		&dcID, &p.Status, &errStr, &uploadedAt)
	if err != nil {
		return nil, err
	}
	if chID.Valid {
		v := chID.Int64
		p.ChannelID = &v
	}
	if msgID.Valid {
		v := int(msgID.Int64)
		p.TelegramMsgID = &v
	}
	if dcID.Valid {
		v := int(dcID.Int64)
		p.DCID = &v
	}
	p.FileIDBlob = blob
	p.Size = size
	if errStr.Valid {
		p.Error = &errStr.String
	}
	p.UploadedAt = fmtTS(uploadedAt)
	return &p, nil
}

// AddPart, yüklenmiş parçayı ekler/günceller (upsert, status=uploaded).
func (s *Store) AddPart(fileID int64, partIndex int, channelID *int64, msgID *int,
	blob []byte, size int64, dcID *int) error {
	_, err := s.db.Exec(
		`INSERT INTO parts (file_id, part_index, channel_id, telegram_msg_id, file_id_blob, size, dc_id, status, uploaded_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,'uploaded',now())
		 ON CONFLICT (file_id, part_index) DO UPDATE SET
		   channel_id=EXCLUDED.channel_id,
		   telegram_msg_id=EXCLUDED.telegram_msg_id,
		   file_id_blob=EXCLUDED.file_id_blob,
		   size=EXCLUDED.size,
		   dc_id=EXCLUDED.dc_id,
		   status='uploaded',
		   uploaded_at=now(),
		   error=NULL`,
		fileID, partIndex, channelID, msgID, blob, size, dcID,
	)
	return err
}

// CreatePendingParts, upload başlarken tüm parça kayıtlarını (sıra) oluşturur.
func (s *Store) CreatePendingParts(fileID int64, partCount int) error {
	for i := 0; i < partCount; i++ {
		if _, err := s.db.Exec(
			`INSERT INTO parts (file_id, part_index, size, status) VALUES ($1,$2,0,'pending')
			 ON CONFLICT (file_id, part_index) DO NOTHING`,
			fileID, i,
		); err != nil {
			return err
		}
	}
	return nil
}

// UpdatePartReceived, tarayıcıdan inen parça boyutunu kaydeder (hâlâ pending).
func (s *Store) UpdatePartReceived(fileID int64, partIndex int, size int64) error {
	_, err := s.db.Exec("UPDATE parts SET size=$1 WHERE file_id=$2 AND part_index=$3", size, fileID, partIndex)
	return err
}

// MarkPartFailed, parçayı failed işaretler.
func (s *Store) MarkPartFailed(fileID int64, partIndex int, errMsg string) error {
	_, err := s.db.Exec("UPDATE parts SET status='failed', error=$1 WHERE file_id=$2 AND part_index=$3", errMsg, fileID, partIndex)
	return err
}

// GetParts, dosyanın tüm parçalarını sırayla döndürür.
func (s *Store) GetParts(fileID int64) ([]Part, error) {
	rows, err := s.db.Query("SELECT "+partCols+" FROM parts WHERE file_id=$1 ORDER BY part_index", fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Part
	for rows.Next() {
		p, err := scanPart(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetPart, dosya + indeks ile parça döndürür.
func (s *Store) GetPart(fileID int64, partIndex int) (*Part, error) {
	p, err := scanPart(s.db.QueryRow(
		"SELECT "+partCols+" FROM parts WHERE file_id=$1 AND part_index=$2", fileID, partIndex,
	))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

// GetPartByID, birincil anahtar ile parça döndürür.
func (s *Store) GetPartByID(id int64) (*Part, error) {
	p, err := scanPart(s.db.QueryRow("SELECT "+partCols+" FROM parts WHERE id=$1", id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

// RefreshPartBlob, resync sırasında blob'u tazeler (status=uploaded).
func (s *Store) RefreshPartBlob(partID int64, msgID int, blob []byte, dcID *int) error {
	_, err := s.db.Exec(
		"UPDATE parts SET file_id_blob=$1, telegram_msg_id=$2, dc_id=$3, status='uploaded', error=NULL WHERE id=$4",
		blob, msgID, dcID, partID,
	)
	return err
}
