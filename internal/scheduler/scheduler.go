// Package scheduler, zamanlanmış görevleri yönetir: günlük +1 kanal, süre sonu
// ve geçici dizin temizliği.
package scheduler

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/proxy-turkey/hyaena-storage/internal/core"
	"github.com/proxy-turkey/hyaena-storage/internal/storage"
	"github.com/proxy-turkey/hyaena-storage/internal/tgworker"
)

// Start, zamanlayıcıyı başlatır:
//   - her gün channelCreationHour'da günlük kanal oluşturur
//   - her dakika süresi dolan dosyaları temizler (DB + Telegram mesajları)
//   - her 15 dakikada sahipsiz geçici parça dizinlerini temizler
func Start(hour int, tw *tgworker.Service, store *storage.Store, tmpRoot string) *cron.Cron {
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

	// her 15 dakikada sahipsiz geçici parça dizinlerini temizle (volume birikmesin)
	if _, err := c.AddFunc("*/15 * * * *", func() {
		sweepTmp(context.Background(), tmpRoot, store)
	}); err != nil {
		log.Printf("Cron kaydı başarısız (tmp temizlik): %v", err)
	}

	c.Start()
	log.Printf("Zamanlayıcı başladı: her gün %02d:00'da +1 kanal; süresi dolan dosyalar her dakika, tmp her 15 dk temizlenir", hour)
	return c
}

// sweepTmp, geçici upload dizinlerini temizler (DB'de kaydı olmayanları siler).
// Volume'un gereksiz parça kalıntılarıyla dolmasını önler.
func sweepTmp(ctx context.Context, tmpRoot string, store *storage.Store) {
	entries, err := os.ReadDir(tmpRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !core.ValidShareToken(e.Name()) {
			continue // session gibi token-olmayan dizinlere dokunma
		}
		f, _ := store.GetFileByToken(e.Name())
		if f == nil {
			_ = os.RemoveAll(filepath.Join(tmpRoot, e.Name()))
			log.Printf("Sahipsiz tmp dizini temizlendi: %s", e.Name())
		}
	}
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
