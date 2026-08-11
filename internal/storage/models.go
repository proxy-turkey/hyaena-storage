package storage

// Channel, channels tablosu satırı.
type Channel struct {
	ID         int64  `json:"id"`
	TelegramID int64  `json:"telegram_id"`
	AccessHash int64  `json:"access_hash"`
	Title      string `json:"title"`
	CreatedDay string `json:"created_day"`
	CreatedAt  string `json:"created_at"`
}

// File, files tablosu satırı.
type File struct {
	ID           int64   `json:"id"`
	Token        string  `json:"token"`
	OriginalName string  `json:"original_name"`
	Size         int64   `json:"size"`
	Mime         string  `json:"mime"`
	PartCount    int     `json:"part_count"`
	DoneParts    int     `json:"done_parts"`
	Status       string  `json:"status"`
	Error        *string `json:"error"`
	CreatedAt    string  `json:"created_at"`
	ReadyAt      *string `json:"ready_at"`
	ExpiresAt    *string `json:"expires_at"`
}

// Part, parts tablosu satırı.
type Part struct {
	ID            int64   `json:"id"`
	FileID        int64   `json:"file_id"`
	PartIndex     int     `json:"part_index"`
	ChannelID     *int64  `json:"channel_id"`
	TelegramMsgID *int    `json:"telegram_msg_id"`
	FileIDBlob    []byte  `json:"file_id_blob"`
	Size          int64   `json:"size"`
	DCID          *int    `json:"dc_id"`
	Status        string  `json:"status"`
	Error         *string `json:"error"`
	UploadedAt    *string `json:"uploaded_at"`
}

// FileRef, parts.file_id_blob içinde JSON olarak saklanan Telegram document referansı.
// (Python'daki pack_bot_file_id'nin Go karşılığı — raw document alanları.)
type FileRef struct {
	ID            int64  `json:"id"`
	AccessHash    int64  `json:"access_hash"`
	FileReference []byte `json:"file_reference"`
	DCID          int    `json:"dc_id"`
}
