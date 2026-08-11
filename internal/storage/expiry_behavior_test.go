package storage

import (
	"testing"
	"time"
)

func TestListExpiredFilesOnlyExpired(t *testing.T) {
	s := newTestStore(t)
	// süresiz dosya (NULL expires_at)
	s.CreateFile("suresiz_abc123", "a.bin", 10, "x", 1, "")
	// geçmişte süresi dolan + ready (silinmeli)
	past := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")
	fid2, _ := s.CreateFile("sureli_past123", "b.bin", 20, "x", 1, past)
	s.UpdateFileStatus(fid2, "ready", nil)
	// geçmişte süresi dolan ama uploading (silinmemeli — muhafazakar)
	s.CreateFile("sureli_uploading", "c.bin", 30, "x", 1, past)

	expired, err := s.ListExpiredFiles(time.Now().UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].OriginalName != "b.bin" {
		t.Errorf("süresi dolan = %d dosya (beklenen sadece ready b.bin): %+v", len(expired), expired)
	}
}
