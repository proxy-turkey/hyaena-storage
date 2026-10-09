package tgworker

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gotd/td/tg"
)

// NotifyOwner, hesap sahibine (Saved Messages) uyarı mesajı gönderir.
// İzleme mimarisinin L2 katmanı (B-02/B-04): dış servise gerek kalmadan
// uyarı doğrudan sahibin Telegram'ına düşer.
func (s *Service) NotifyOwner(ctx context.Context, text string) error {
	if err := s.apiGuard(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerSelf{},
		Message:  text,
		RandomID: time.Now().UnixNano(),
	})
	if err != nil {
		return fmt.Errorf("sahip bildirimi gönderilemedi: %w", err)
	}
	return nil
}

// NotifyOwnerBestEffort, bağlamı 5 sn ile sınırlayıp en iyi şekilde bildirim
// atar; hata yalnızca loglanır (çağıran akışı engellenmemeli).
func (s *Service) NotifyOwnerBestEffort(text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.NotifyOwner(ctx, text); err != nil {
		log.Printf("Sahip bildirimi başarısız: %v", err)
	}
}

// ---- FloodWait istatistiği (B-07: Telegram hesap limitlerinin izlenmesi) ----

// floodTracker, kayan 24 saatlik FloodWait olaylarını tutar.
type floodTracker struct {
	mu     sync.Mutex
	events []floodEvent
}

type floodEvent struct {
	at   time.Time
	wait time.Duration
}

// record, olayı kaydeder ve 24 saatten eski olayları düşürür.
func (f *floodTracker) record(wait time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cutoff := time.Now().Add(-24 * time.Hour)
	kept := f.events[:0]
	for _, e := range f.events {
		if e.at.After(cutoff) {
			kept = append(kept, e)
		}
	}
	f.events = append(kept, floodEvent{at: time.Now(), wait: wait})
}

// stats, son 24 saatteki olay sayısı ve en son bekleme süresini döndürür.
func (f *floodTracker) stats() (count24h int, lastWait time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cutoff := time.Now().Add(-24 * time.Hour)
	kept := f.events[:0]
	for _, e := range f.events {
		if e.at.After(cutoff) {
			kept = append(kept, e)
		}
	}
	f.events = kept
	if len(f.events) == 0 {
		return 0, 0
	}
	return len(f.events), f.events[len(f.events)-1].wait
}

// FloodWaitStats, son 24 saatteki FloodWait sayısını ve son bekleme süresini
// döndürür (admin özet panelinde gösterilir).
func (s *Service) FloodWaitStats() (int, time.Duration) {
	return s.flood.stats()
}
