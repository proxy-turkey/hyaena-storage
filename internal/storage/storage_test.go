package storage

import (
	"testing"
	"time"
)

func TestChannelCRUD(t *testing.T) {
	s := newTestStore(t)

	// yeni kanal ekle
	id1, err := s.CreateChannel(-1001, 111, "Storage 2026-08-11", "2026-08-11")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := s.CountChannels()
	if n != 1 {
		t.Errorf("count = %d, want 1", n)
	}

	// ikinci kanal → sayı 2
	_, err = s.CreateChannel(-1002, 222, "Storage 2026-08-10", "2026-08-10")
	if err != nil {
		t.Fatal(err)
	}
	n, _ = s.CountChannels()
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}

	// get by id
	c, err := s.GetChannel(id1)
	if err != nil || c == nil {
		t.Fatalf("GetChannel: %v %v", c, err)
	}
	if c.TelegramID != -1001 || c.CreatedDay != "2026-08-11" {
		t.Errorf("kanal alanları yanlış: %+v", c)
	}

	// channel_exists_for_day
	ok, _ := s.ChannelExistsForDay("2026-08-11")
	if !ok {
		t.Error("2026-08-11 kanalı var olmalı")
	}
	ok, _ = s.ChannelExistsForDay("2026-08-01")
	if ok {
		t.Error("2026-08-01 kanalı olmamalı")
	}

	// list
	chs, err := s.ListChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 2 {
		t.Errorf("listelenen = %d, want 2", len(chs))
	}
}

func TestFilePartsCRUD(t *testing.T) {
	s := newTestStore(t)
	chID, _ := s.CreateChannel(-1001, 111, "Kanal", "2026-08-11")

	fid, err := s.CreateFile("testtoken_abcdefgh", "x.bin", 100, "application/octet-stream", 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePendingParts(fid, 2); err != nil {
		t.Fatal(err)
	}
	msgID := 500
	dc := 2
	blob := []byte("BLOB")
	if err := s.AddPart(fid, 0, &chID, &msgID, blob, 50, &dc); err != nil {
		t.Fatal(err)
	}
	parts, err := s.GetParts(fid)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("parça = %d, want 2", len(parts))
	}
	if parts[0].Status != "uploaded" {
		t.Errorf("part0 status = %s, want uploaded", parts[0].Status)
	}
	if parts[1].Status != "pending" {
		t.Errorf("part1 status = %s, want pending", parts[1].Status)
	}

	// ready durumu: done_parts = part_count
	if err := s.UpdateFileStatus(fid, "ready", nil); err != nil {
		t.Fatal(err)
	}
	f, err := s.GetFileByToken("testtoken_abcdefgh")
	if err != nil || f == nil {
		t.Fatalf("GetFileByToken: %v %v", f, err)
	}
	if f.Status != "ready" || f.DoneParts != 2 {
		t.Errorf("file ready durumu yanlış: %+v", f)
	}
}

func TestDeleteCascade(t *testing.T) {
	s := newTestStore(t)
	fid, _ := s.CreateFile("deletetoken_xyz123", "d.bin", 10, "x", 1, "")
	if err := s.CreatePendingParts(fid, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFile(fid); err != nil {
		t.Fatal(err)
	}
	f, err := s.GetFileByID(fid)
	if err != nil || f != nil {
		t.Errorf("silinen dosya bulundu: %v", f)
	}
	parts, err := s.GetParts(fid)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 0 {
		t.Errorf("cascade sonrası parça = %d, want 0", len(parts))
	}
}

func TestPickUploadChannelsBalanced(t *testing.T) {
	s := newTestStore(t)
	ids := []int64{1, 2, 3}

	// 2 parçalı dosya: sayaç 0'dan başlar → parça0→1, parça1→2
	plan, err := s.PickUploadChannelsBalanced(ids, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 || plan[0] != 1 || plan[1] != 2 {
		t.Errorf("plan1 = %v, want [1 2]", plan)
	}

	// sonraki dosya (1 parça): sayaç 2'de → parça0→3 (kanal-3, eşit dağılım!)
	plan2, err := s.PickUploadChannelsBalanced(ids, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan2) != 1 || plan2[0] != 3 {
		t.Errorf("plan2 = %v, want [3] (kanal-3)", plan2)
	}

	// wrap: 3 parçalı dosya sayaç 3'ten → [1,2,3]
	plan3, err := s.PickUploadChannelsBalanced(ids, 3)
	if err != nil {
		t.Fatal(err)
	}
	want3 := []int64{1, 2, 3}
	for i := range want3 {
		if plan3[i] != want3[i] {
			t.Errorf("plan3[%d] = %d, want %d", i, plan3[i], want3[i])
		}
	}

	// dosya sayısı > kanal sayısı: wrap sarmalı
	plan4, err := s.PickUploadChannelsBalanced(ids, 5) // sayaç 6'dan → [1,2,3,1,2]
	if err != nil {
		t.Fatal(err)
	}
	if len(plan4) != 5 {
		t.Fatalf("plan4 len = %d, want 5", len(plan4))
	}
	for i, want := range []int64{1, 2, 3, 1, 2} {
		if plan4[i] != want {
			t.Errorf("plan4[%d] = %d, want %d", i, plan4[i], want)
		}
	}
}

func TestPickUploadChannelsBalancedEmpty(t *testing.T) {
	s := newTestStore(t)
	plan, err := s.PickUploadChannelsBalanced(nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if plan != nil {
		t.Errorf("boş liste nil olmalı, got %v", plan)
	}
}

func TestExpiredFiles(t *testing.T) {
	s := newTestStore(t)
	// süresiz dosya
	fid1, _ := s.CreateFile("expires_none_123", "suresiz.bin", 100, "x", 1, "")
	// 1 saat süreli (geçmişte biten) + ready
	past := time.Now().Add(-time.Hour).Format("2006-01-02 15:04:05")
	fid2, _ := s.CreateFile("expires_past_123", "gecmis.bin", 200, "x", 1, past)
	s.UpdateFileStatus(fid2, "ready", nil)
	// gelecekte biten
	future := time.Now().Add(24 * time.Hour).Format("2006-01-02 15:04:05")
	fid3, _ := s.CreateFile("expires_future_1", "gelecek.bin", 300, "x", 1, future)

	expired, err := s.ListExpiredFiles(time.Now().Format("2006-01-02 15:04:05"))
	if err != nil {
		t.Fatal(err)
	}
	// sadece fid2 (geçmiş) süresi dolmuş olmalı
	if len(expired) != 1 {
		t.Fatalf("süresi dolan = %d, want 1 (sadece geçmiş)", len(expired))
	}
	if expired[0].ID != fid2 {
		t.Errorf("süresi dolan = %d, want %d", expired[0].ID, fid2)
	}
	// fid1 ve fid3 hâlâ durmalı
	if _, err := s.GetFileByID(fid1); err != nil {
		t.Errorf("süresiz dosya kayboldu")
	}
	if _, err := s.GetFileByID(fid3); err != nil {
		t.Errorf("gelecek dosya kayboldu")
	}
}

func TestSummaryQueries(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateFile("tok1_abcdefghijkl", "a.bin", 100, "x", 1, ""); err != nil {
		t.Fatal(err)
	}
	fid2, _ := s.CreateFile("tok2_abcdefghijkl", "b.bin", 200, "x", 1, "")
	if err := s.UpdateFileStatus(fid2, "ready", nil); err != nil {
		t.Fatal(err)
	}
	usage, err := s.StorageUsage()
	if err != nil {
		t.Fatal(err)
	}
	if usage != 200 {
		t.Errorf("usage = %d, want 200 (sadece ready)", usage)
	}
	n, _ := s.CountFiles()
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
}
