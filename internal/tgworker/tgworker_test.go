package tgworker

import (
	"testing"
	"time"

	gotdTgerr "github.com/gotd/td/tgerr"
	"github.com/gotd/td/tg"
)

// B-08: tgworker paketi için birim testler — Telegram'a bağlanmadan
// saf fonksiyonlar ve durum makineleri test edilir.

func TestIsFileRefExpired(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"expired", &gotdTgerr.Error{Code: 400, Message: "FILE_REFERENCE_EXPIRED"}, true},
		{"file-part", &gotdTgerr.Error{Code: 400, Message: "FILE_PART_1_MISSING"}, true},
		// FILE_ önekli başka hata → kapsayıcı kabul (resync zararsızdır)
		{"file-kapsayici", &gotdTgerr.Error{Code: 400, Message: "FILE_MIGRATE_2"}, true},
		// FILE ile başlayan ama FILE_ öneki taşımayan hata → kapsam dışı
		{"filename-invalid kapsam dışı", &gotdTgerr.Error{Code: 400, Message: "FILENAME_INVALID"}, false},
		{"auth-hatası değil", &gotdTgerr.Error{Code: 401, Message: "AUTH_KEY_UNREGISTERED"}, false},
		{"400 ama FILE değil", &gotdTgerr.Error{Code: 400, Message: "CHAT_WRITE_FORBIDDEN"}, false},
	}
	for _, c := range cases {
		if got := isFileRefExpired(c.err); got != c.want {
			t.Errorf("%s: isFileRefExpired = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestExtractDocument(t *testing.T) {
	// medyasız mesaj → hata
	if _, err := extractDocument(&tg.Message{}); err == nil {
		t.Error("medyasız mesaj hata vermeli")
	}
	// fotoğraf medyası → hata (document değil)
	photo := &tg.Message{Media: &tg.MessageMediaPhoto{}}
	if _, err := extractDocument(photo); err == nil {
		t.Error("photo medya hata vermeli")
	}
	// geçerli document
	doc := tg.Document{ID: 7, AccessHash: 11}
	msg := &tg.Message{Media: &tg.MessageMediaDocument{Document: &doc}}
	got, err := extractDocument(msg)
	if err != nil || got == nil || got.ID != 7 {
		t.Errorf("document çıkarılamadı: err=%v got=%v", err, got)
	}
	// MessageMediaDocument.documentEmpty → hata
	empty := &tg.Message{Media: &tg.MessageMediaDocument{Document: &tg.DocumentEmpty{}}}
	if _, err := extractDocument(empty); err == nil {
		t.Error("documentEmpty hata vermeli")
	}
}

func TestExtractMessageVariants(t *testing.T) {
	// UpdatesCombined içinde kanal mesajı (upload yanıtlarının gerçek biçimi)
	upd := &tg.UpdateNewChannelMessage{Message: &tg.Message{ID: 99}}
	comb := &tg.UpdatesCombined{Updates: []tg.UpdateClass{upd}}
	if m, ok := extractMessage(comb); !ok || m.ID != 99 {
		t.Errorf("UpdatesCombined/kanal mesajı çözümlenemedi: %v %v", m, ok)
	}
	// UpdateShortMessage (kendi kendine mesaj — NotifyOwner yanıtı bu biçimde dönebilir)
	short := &tg.UpdateShortMessage{ID: 5, Out: true}
	if m, ok := extractMessage(short); !ok || m.ID != 5 {
		t.Errorf("UpdateShortMessage çözümlenemedi: %v %v", m, ok)
	}
	// ilgisiz update → false
	if _, ok := extractMessage(&tg.UpdatesCombined{Updates: []tg.UpdateClass{&tg.UpdateDeleteMessages{}}}); ok {
		t.Error("ilgisiz update mesaj döndürmemeli")
	}
}

func TestFloodTrackerRolling24h(t *testing.T) {
	f := &floodTracker{}
	f.record(30 * time.Second)
	f.record(60 * time.Second)
	n, last := f.stats()
	if n != 2 || last != 60*time.Second {
		t.Errorf("stats = (%d, %v), want (2, 60s)", n, last)
	}
	// 24 saatten eski olay düşürülür
	f.events[0].at = time.Now().Add(-25 * time.Hour)
	n, _ = f.stats()
	if n != 1 {
		t.Errorf("eski olay düşürülmedi: n=%d, want 1", n)
	}
}

func TestFloodTrackerConcurrent(t *testing.T) {
	f := &floodTracker{}
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				f.record(time.Second)
				f.stats()
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if n, _ := f.stats(); n != 400 {
		t.Errorf("yarış sonrası sayı = %d, want 400", n)
	}
}
