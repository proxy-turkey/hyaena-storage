package tgworker

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gotd/td/tg"
)

// todayLocal, yerel saat ile YYYY-MM-DD döndürür.
func todayLocal() string {
	return time.Now().Format("2006-01-02")
}

// titleForDay, kanal adını üretir (marka: Hyaena).
func titleForDay(day string) string {
	return "Hyaena " + day
}

// RenameChannel, kanalın Telegram'daki adını ve DB kaydını günceller.
func (s *Service) RenameChannel(ctx context.Context, channelID int64, newTitle string) error {
	if err := s.apiGuard(ctx); err != nil {
		return err
	}
	ch, err := s.db.GetChannel(channelID)
	if err != nil || ch == nil {
		return fmt.Errorf("kanal bulunamadı: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.call(ctx, func() error {
		_, err := s.api.ChannelsEditTitle(ctx, &tg.ChannelsEditTitleRequest{
			Channel: s.inputChannel(*ch),
			Title:   newTitle,
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("kanal adı değiştirilemedi: %w", err)
	}
	// DB'yi güncelle
	return s.db.UpdateChannelTitle(channelID, newTitle)
}

// createChannelLocked, kilit altında kanal oluşturur ve DB'ye kaydeder.
func (s *Service) createChannelLocked(ctx context.Context, title, day string) (int64, error) {
	var updates tg.UpdatesClass
	err := s.call(ctx, func() error {
		var e error
		updates, e = s.api.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{
			Title:     title,
			About:     "Otomatik depolama kanalı",
			Megagroup: false,
			Broadcast: true,
		})
		return e
	})
	if err != nil {
		return 0, err
	}

	ch, err := findChannelFromUpdates(updates)
	if err != nil {
		return 0, err
	}
	id, err := s.db.CreateChannel(ch.ID, ch.AccessHash, title, day)
	if err != nil {
		return 0, err
	}
	s.entityCache[id] = &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}
	log.Printf("Kanal oluşturuldu: %s (tg=%d)", title, ch.ID)
	return id, nil
}

// findChannelFromUpdates, Updates yanıtından *tg.Channel çıkarır.
func findChannelFromUpdates(updates tg.UpdatesClass) (*tg.Channel, error) {
	var chats []tg.ChatClass
	switch u := updates.(type) {
	case *tg.Updates:
		chats = u.Chats
	case *tg.UpdatesCombined:
		chats = u.Chats
	default:
		return nil, fmt.Errorf("beklenmeyen Updates tipi: %T", updates)
	}
	for _, c := range chats {
		if notEmpty, ok := c.AsNotEmpty(); ok && notEmpty != nil {
			if ch, ok := notEmpty.(*tg.Channel); ok {
				return ch, nil
			}
		}
	}
	return nil, fmt.Errorf("kanal bulunamadı")
}

// EnsureDailyChannel, bugünün kanalı yoksa +1 oluşturur. Dönen: db id veya 0.
func (s *Service) EnsureDailyChannel(ctx context.Context) error {
	if err := s.apiGuard(ctx); err != nil {
		return err
	}
	day := todayLocal()
	// Exists-check kilidin İÇİNDE: EnsureFleet + zamanlayıcı aynı anda çağırırsa
	// ikisi de check'i geçip aynı gün çift kanal açmasın (check-then-act).
	s.mu.Lock()
	defer s.mu.Unlock()
	exists, err := s.db.ChannelExistsForDay(day)
	if err != nil {
		return err
	}
	if exists {
		return nil // bugünün kanalı zaten var
	}
	_, err = s.createChannelLocked(ctx, titleForDay(day), day)
	if err != nil {
		log.Printf("Günlük kanal oluşturulamadı: %v", err)
	}
	return nil
}

// bootstrapChannels, filo boşsa BOOTSTRAP_CHANNELS kadar kanal açar.
func (s *Service) bootstrapChannels(ctx context.Context) error {
	n, err := s.db.CountChannels()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil // filo var → hedefe tamamlama yok (peg)
	}
	target := s.s.BootstrapChannels
	if target < 1 {
		target = 1
	}

	chs, err := s.db.ListChannels()
	if err != nil {
		return err
	}
	used := map[string]bool{}
	for _, c := range chs {
		used[c.CreatedDay] = true
	}
	var days []string
	today := todayLocal()
	for d := today; len(days) < target; d = addDays(d, -1) {
		if !used[d] {
			days = append(days, d)
		}
	}

	for _, day := range days {
		count, _ := s.db.CountChannels()
		if count >= target {
			break
		}
		s.mu.Lock()
		_, err := s.createChannelLocked(ctx, titleForDay(day), day)
		s.mu.Unlock()
		if err != nil {
			return err
		}
		count, _ = s.db.CountChannels()
		if count < target {
			time.Sleep(time.Duration(s.s.ChannelIntervalSN * float64(time.Second)))
		}
	}
	return nil
}

// EnsureFleet, bootstrap (boşsa) + günlük kanalı garanti eder.
func (s *Service) EnsureFleet(ctx context.Context) error {
	if err := s.apiGuard(ctx); err != nil {
		return err
	}
	if err := s.bootstrapChannels(ctx); err != nil {
		log.Printf("bootstrap hatası: %v", err)
	}
	return s.EnsureDailyChannel(ctx)
}

// addDays, YYYY-MM-DD tarihine gün ekler.
func addDays(day string, delta int) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, delta).Format("2006-01-02")
}
