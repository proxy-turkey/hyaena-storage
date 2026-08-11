package tgworker

import "sync"

// UploadProgress, bir dosyanın canlı upload ilerlemesi.
type UploadProgress struct {
	PartCount   int                `json:"part_count"`
	SentBytes   int64              `json:"sent_bytes"`
	TotalBytes  int64              `json:"total_bytes"`
	SegProgress map[int]float64    `json:"segment_progress"`
}

// ProgressStore, mutex'li upload ilerleme kaydı.
type ProgressStore struct {
	mu    sync.Mutex
	items map[int64]*UploadProgress
}

// NewProgressStore yeni bir kayıt deposu oluşturur.
func NewProgressStore() *ProgressStore {
	return &ProgressStore{items: map[int64]*UploadProgress{}}
}

// Register, upload başlangıcında kaydı oluşturur.
func (p *ProgressStore) Register(fileID int64, partCount int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.items[fileID] = &UploadProgress{PartCount: partCount, SegProgress: map[int]float64{}}
}

// SetTotal, toplam boyutu ayarlar.
func (p *ProgressStore) SetTotal(fileID, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if it, ok := p.items[fileID]; ok {
		it.TotalBytes = total
	}
}

// AddSent, gönderilen boyutu artırır.
func (p *ProgressStore) AddSent(fileID, n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if it, ok := p.items[fileID]; ok {
		it.SentBytes += n
	}
}

// Get, kaydı kopya olarak döndürür (yoksa nil).
func (p *ProgressStore) Get(fileID int64) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	it, ok := p.items[fileID]
	if !ok {
		return nil
	}
	seg := map[int]float64{}
	for k, v := range it.SegProgress {
		seg[k] = v
	}
	return map[string]any{
		"part_count":       it.PartCount,
		"sent_bytes":       it.SentBytes,
		"total_bytes":      it.TotalBytes,
		"segment_progress": seg,
	}
}

// Clear, kaydı siler.
func (p *ProgressStore) Clear(fileID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.items, fileID)
}
