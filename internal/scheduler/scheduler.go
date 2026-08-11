// Package scheduler, zamanlanmış görevleri yönetir: günlük +1 kanal ve süre sonu temizliği.
package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"tgshare/internal/storage"
	"tgshare/internal/tgworker"
)

// Start, zamanlayıcıyı başlatır:
//   - her gün channelCreationHour'da günlük kanal oluşturur
//   - her dakika süresi dolan dosyaları temizler (DB + Telegram mesajları)
func Start(hour int, tw *tgworker.Service, store *storage.Store) *cron.Cron {
	c := cron.New(cron.WithLocation(time.Local))

	// günlük +1 kanal
	expr := fmt.Sprintf("0 %d * * *", hour)
	if _, err := c.AddFunc(expr, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := tw.EnsureDailyChannel(ctx); err != nil {
			log.Printf("Günlük kanal işi başarısız: %v", err)
		}
	}); err != nil {
		log.Printf("Cron kaydı başarısız (kanal): %v", err)
	}

	// her dakika süresi dolan dosyaları temizle
	if _, err := c.AddFunc("*/1 * * * *", func() {
		expireFiles(context.Background(), tw, store)
	}); err != nil {
		log.Printf("Cron kaydı başarısız (süre sonu): %v", err)
	}

	c.Start()
	log.Printf("Zamanlayıcı başladı: her gün %02d:00'da +1 kanal; süresi dolan dosyalar her dakika silinir", hour)
	return c
}

// expireFiles, süresi dolmuş dosyaları bulur, Telegram mesajlarını siler ve DB'den kaldırır.
func expireFiles(ctx context.Context, tw *tgworker.Service, store *storage.Store) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	files, err := store.ListExpiredFiles(now)
	if err != nil {
		log.Printf("Süresi dolan dosyalar okunamadı: %v", err)
		return
	}
	for _, f := range files {
		parts, err := store.GetParts(f.ID)
		if err == nil {
			for _, p := range parts {
				if p.TelegramMsgID != nil && tw != nil {
					if err := tw.DeleteSegmentMessage(ctx, p); err != nil {
						log.Printf("Süreli dosya mesajı silinemedi (file=%d part=%d): %v", f.ID, p.PartIndex, err)
					}
				}
			}
		}
		if err := store.DeleteFile(f.ID); err != nil {
			log.Printf("Süresi dolan dosya silinemedi (id=%d): %v", f.ID, err)
			continue
		}
		log.Printf("Süresi dolan dosya silindi: %s (%s)", f.OriginalName, f.Token)
	}
}
