// Package scheduler, zamanlanmış görevleri yönetir: günlük +1 kanal, süre sonu
// ve geçici dizin temizliği.
package scheduler

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
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
func Start(hour int, tw *tgworker.Service, store *storage.Store, tmpRoot string, purgeExpired func(token, name string)) *cron.Cron {
	// C20: aynı iş hâlâ çalışıyorsa yeni tetikleme atlanır — örtüşen dakikalık
	// expiry koşuları global Telegram mutex kuyruğunda üst üste binemez.
	c := cron.New(cron.WithLocation(time.Local), cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)))

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

	// her dakika süresi dolan dosyaları temizle (C20: koşu-başı 50sn bütçe —
	// dakika aşılırsa kalan dosyalar sonraki sweep'te idempotent devam eder)
	if _, err := c.AddFunc("*/1 * * * *", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
		defer cancel()
		expireFiles(ctx, tw, store, purgeExpired)
	}); err != nil {
		log.Printf("Cron kaydı başarısız (süre sonu): %v", err)
	}

	// her 15 dakikada sahipsiz geçici parça dizinlerini temizle (volume birikmesin)
	if _, err := c.AddFunc("*/15 * * * *", func() {
		sweepTmp(context.Background(), tmpRoot, store, tw)
	}); err != nil {
		log.Printf("Cron kaydı başarısız (tmp temizlik): %v", err)
	}

	// her gün referans tazeleme: Telegram file_reference'ları ~günler içinde
	// bayatlar; bayat parça ilk indirmede resync gerektirir (yavaşlama/kesilme).
	// Kanal işinden 1 saat sonra çalışır, tüm parçaları sırayla tazeler.
	refreshExpr := fmt.Sprintf("0 %d * * *", (hour+1)%24)
	if _, err := c.AddFunc(refreshExpr, func() {
		refreshReferences(tw, store)
	}); err != nil {
		log.Printf("Cron kaydı başarısız (referans tazeleme): %v", err)
	}

	// her 15 dakikada disk doluluk kontrolü (B-04): %85 üstünde sahibi
	// Telegram'dan uyarılır; sessiz kalması için uyarı günde bir kez yinelenir.
	if _, err := c.AddFunc("*/15 * * * *", func() {
		checkDisk(tw, "/")
	}); err != nil {
		log.Printf("Cron kaydı başarısız (disk kontrol): %v", err)
	}

	c.Start()
	log.Printf("Zamanlayıcı başladı: her gün %02d:00'da +1 kanal, %02d:00'da referans tazeleme; süresi dolan dosyalar her dakika, tmp her 15 dk, disk kontrolü her 15 dk yapılır", hour, (hour+1)%24)
	return c
}

// diskAlertState, uyarı tekrarını günde bir ile sınırlar; disk %80 altına
// inince sıfırlanır (düzelen durumda tekrar haber verilebilsin).
var diskAlertState struct {
	lastAlert time.Time
	alerting  bool
	mu        sync.Mutex
}

// checkDisk, kök dosya sisteminin doluluğunu ölçer; eşik ihlalinde sahibi
// uyarır (B-04). Telegram worker hazır değilse yalnızca loglanır.
func checkDisk(tw *tgworker.Service, path string) {
	pct, err := core.DiskUsagePct(path)
	if err != nil {
		return
	}
	diskAlertState.mu.Lock()
	defer diskAlertState.mu.Unlock()
	if pct >= 85 {
		if !diskAlertState.alerting || time.Since(diskAlertState.lastAlert) > 24*time.Hour {
			log.Printf("DİSK UYARISI: %s doluluk %%%.1f (eşik 85)", path, pct)
			if tw != nil {
				go tw.NotifyOwnerBestEffort(fmt.Sprintf(
					"⚠️ Hyaena Storage: disk doluluğu %%%.0f (eşik %%85). Yer açın; disk dolarsa upload ve DB yazma durur.", pct))
			}
			diskAlertState.alerting = true
			diskAlertState.lastAlert = time.Now()
		}
	} else if pct < 80 {
		diskAlertState.alerting = false
	}
}

// refreshReferences, tüm yüklenmiş parçaların file_reference'ını mesaj id'lerinden
// tazeler. ResyncPart parça başına tek ucuz RPC yapar (ChannelsGetMessages).
func refreshReferences(tw *tgworker.Service, store *storage.Store) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	parts, err := store.ListUploadedParts()
	if err != nil {
		log.Printf("Referans tazeleme: parçalar okunamadı: %v", err)
		return
	}
	ok, skip := 0, 0
	for _, p := range parts {
		if ctx.Err() != nil {
			break
		}
		done, err := tw.ResyncPart(ctx, p.ID)
		if err != nil {
			log.Printf("Referans tazeleme hatası (part=%d): %v", p.ID, err)
			skip++
			continue
		}
		if done {
			ok++
		} else {
			skip++
		}
	}
	log.Printf("Referans tazeleme tamam: %d tazelendi, %d atlandı", ok, skip)
}

// sweepTmp, geçici upload dizinlerini temizler (volume asla dolmasın).
// Kural:
//  1. DB'de kaydı olmayan token dizinleri (silinen dosyanın kalıntısı)
//  2. DB kaydı 'uploading' ama dizini eski (yarım kalmış upload) — >24 saat
//  3. DB kaydı 'uploading' ama tmp dizini hiç yoksa (terk edilmiş) — kaydı da sil
func sweepTmp(ctx context.Context, tmpRoot string, store *storage.Store, tw *tgworker.Service) {
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
		dirPath := filepath.Join(tmpRoot, e.Name())
		f, err := store.GetFileByToken(e.Name())
		if err != nil {
			// DB hatası → dizini silme (aktif upload'un tmp'si kaybolmasın)
			continue
		}

		if f == nil {
			// DB'de kayıt yok → kalıntı, sil
			_ = os.RemoveAll(dirPath)
			log.Printf("Sahipsiz tmp dizini temizlendi: %s", e.Name())
			continue
		}

		// DB'de 'uploading' durumunda ama eski → yarım kalmış upload, temizle.
		// F-14: satır düşürülmeden önce kayıtlı segment mesajları best-effort
		// silinir — cascade msg_id'leri yok ettiği için sonrası mümkün değil.
		if f.Status == "uploading" {
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 24*time.Hour {
				if tw != nil {
					if parts, perr := store.GetParts(f.ID); perr == nil {
						for _, p := range parts {
							if p.TelegramMsgID != nil {
								if derr := tw.DeleteSegmentMessage(context.Background(), p); derr != nil {
									log.Printf("Sweep: segment mesajı silinemedi (file=%d part=%d): %v", f.ID, p.PartIndex, derr)
								}
							}
						}
					}
				}
				_ = os.RemoveAll(dirPath)
				_ = store.DeleteFile(f.ID)
				log.Printf("Yarım kalmış upload temizlendi: %s (%s)", f.OriginalName, e.Name())
			}
		}
	}
}

// expireFiles, süresi dolmuş dosyaları bulur, Telegram mesajlarını siler ve DB'den kaldırır.
func expireFiles(ctx context.Context, tw *tgworker.Service, store *storage.Store, purgeExpired func(token, name string)) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	files, err := store.ListExpiredFiles(now)
	if err != nil {
		log.Printf("Süresi dolan dosyalar okunamadı: %v", err)
		return
	}
	for _, f := range files {
		parts, err := store.GetParts(f.ID)
		if err != nil {
			// F-15: parçalar okunamadıysa satır düşürmek msg_id'leri yok eder —
			// satırı koru, sonraki dakikalık sweep'te (idempotent) tekrar dene.
			log.Printf("Süreli dosya parçaları okunamadı (id=%d): %v", f.ID, err)
			continue
		}
		failed := 0
		for _, p := range parts {
			if p.TelegramMsgID != nil && tw != nil {
				pctx, pcancel := context.WithTimeout(ctx, 25*time.Second)
				err := tw.DeleteSegmentMessage(pctx, p)
				pcancel()
				if err != nil {
					failed++
					log.Printf("Süreli dosya mesajı silinemedi (file=%d part=%d): %v", f.ID, p.PartIndex, err)
				}
			}
		}
		// F-15: Telegram'dan silinemeyen içerik yetim kalmasın — DB satırı
		// (ve msg_id kayıtları) yalnızca TÜM parçalar silindiğinde düşer.
		if failed > 0 {
			continue
		}
		if err := store.DeleteFile(f.ID); err != nil {
			log.Printf("Süresi dolan dosya silinemedi (id=%d): %v", f.ID, err)
			continue
		}
		// F-07: süre sonu = silme garantisi — edge cache'ten de düşür.
		if purgeExpired != nil {
			purgeExpired(f.Token, f.OriginalName)
		}
		log.Printf("Süresi dolan dosya silindi: %s (%s)", f.OriginalName, f.Token)
	}
}
