// Telegram Storage — GoMTProto + chi tabanlı Telegram cloud depolama servisi.
// Python (FastAPI + Telethon) sürümünün birebir Go karşılığı.
package main

import (
	"context"
	"embed"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/proxy-turkey/hyaena-storage/internal/config"
	"github.com/proxy-turkey/hyaena-storage/internal/core"
	"github.com/proxy-turkey/hyaena-storage/internal/httpapi"
	"github.com/proxy-turkey/hyaena-storage/internal/scheduler"
	"github.com/proxy-turkey/hyaena-storage/internal/storage"
	"github.com/proxy-turkey/hyaena-storage/internal/tgworker"
)

//go:embed static
var staticFS embed.FS

func main() {
	// --login: ayrı bir interaktif login adımı (servisi başlatmadan)
	if len(os.Args) > 1 && os.Args[1] == "--login" {
		runLogin()
		return
	}

	root, err := os.Getwd()
	if err != nil {
		log.Fatalf("çalışma dizini alınamadı: %v", err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		log.Fatalf("yapılandırma hatası: %v", err)
	}
	if cfg.AdminPassword == "" || cfg.AdminPassword == "degistir-ben" {
		log.Println("UYARI: ADMIN_PASSWORD hâlâ varsayılan! .env'den değiştir.")
	}

	// DB
	store, err := storage.Open(cfg.DBFile())
	if err != nil {
		log.Fatalf("veritabanı açılamadı: %v", err)
	}
	defer store.Close()

	// geçici dizin + eski temizlik
	if err := os.MkdirAll(cfg.TmpDir(), 0o755); err != nil {
		log.Fatalf("tmp dizini oluşturulamadı: %v", err)
	}
	sweepOrphanTmp(cfg.TmpDir(), store, 24*time.Hour)

	// Telegram worker + canlılık gözcüsü (RPC'ler ölürse süreç systemd ile
	// taze başlar — bkz. tgworker.RunHeartbeat)
	tw := tgworker.New(cfg, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tw.Run(ctx)
	go tw.RunHeartbeat(ctx, 5*time.Minute, 3)

	// HTTP
	handler := httpapi.New(ctx, cfg, store, tw, staticFS)
	srv := &http.Server{
		Addr:    cfg.Host + ":" + strconv.Itoa(cfg.Port),
		Handler: handler,
	}

	// günlük +1 kanal + süre sonu + tmp temizliği
	// F-07: süre dolan dosyalar edge cache'ten de düşer (purge callback).
	sch := scheduler.Start(cfg.ChannelCreationHour, tw, store, cfg.TmpDir(), func(token, name string) {
		base := cfg.PublicBaseURL
		if base == "" {
			base = "https://storage.hyaena.co.uk"
		}
		_ = httpapi.PurgeCacheFile(cfg, base+"/api/download/"+token+"/"+url.PathEscape(name))
		_ = httpapi.PurgeCacheFile(cfg, base+"/api/download/"+token)
	})
	defer sch.Stop()

	// HTTP sunucusunu başlat.
	// F-04: başlık okuma ve boşta bağlantı süreleri sınırlı (ReadTimeout/
	// WriteTimeout KONMAZ — 100GB'lık indirme/yükleme akışlarını keser;
	// yavaş-okuyucu koruması download.go'da parça bazlı write deadline ile).
	srv.ReadHeaderTimeout = 15 * time.Second
	srv.IdleTimeout = 120 * time.Second
	go func() {
		if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
			log.Printf("Servis hazır: https://%s:%d (TLS)", cfg.Host, cfg.Port)
			if err := srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("HTTPS sunucusu: %v", err)
			}
			return
		}
		log.Printf("Servis hazır: http://%s:%d", cfg.Host, cfg.Port)
		log.Printf("Not: ilk çalıştırmada Telegram login (telefon + kod) terminalde istenir.")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP sunucusu: %v", err)
		}
	}()

	// İlk Telegram erişimi gerektiren işlem: startup fleet (arka planda)
	go func() {
		<-tw.Ready()
		fleetCtx, fc := context.WithTimeout(context.Background(), 30*time.Minute)
		defer fc()
		if err := tw.EnsureFleet(fleetCtx); err != nil {
			log.Printf("Başlangıç filo hatası: %v", err)
		}
	}()

	// sinyal yakalama
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Kapatılıyor...")
	shutdownCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	_ = srv.Shutdown(shutdownCtx)
	cancel()
}

// runLogin, sadece interaktif Telegram login yapar (servis başlatmaz).
// Kullanım: ./tgshare --login [--phone +90...] [--code 12345] [--password ...]
func runLogin() {
	root, err := os.Getwd()
	if err != nil {
		log.Fatalf("çalışma dizini alınamadı: %v", err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		log.Fatalf("yapılandırma hatası: %v", err)
	}
	store, err := storage.Open(cfg.DBFile())
	if err != nil {
		log.Fatalf("veritabanı açılamadı: %v", err)
	}
	defer store.Close()

	args := tgworker.LoginArgs{}
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--phone":
			if i+1 < len(os.Args) {
				args.Phone = os.Args[i+1]
				i++
			}
		case "--code":
			if i+1 < len(os.Args) {
				args.Code = os.Args[i+1]
				i++
			}
		case "--password":
			if i+1 < len(os.Args) {
				args.Password = os.Args[i+1]
				i++
			}
		}
	}

	tw := tgworker.New(cfg, store)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	log.Println("Telegram login başlatılıyor...")
	if err := tw.LoginOnce(ctx, args); err != nil {
		log.Fatalf("Login başarısız: %v", err)
	}
	log.Printf("Login tamamlandı. Session dosyası: %s", cfg.SessionFile)
	log.Println("Şimdi servisi başlatabilirsin: ./tgshare")
}

// sweepOrphanTmp, sahipsiz geçici upload dizinlerini temizler.
// İki kural:
//  1. DB'de kaydı olmayan token dizinleri (silinen dosyanın kalıntısı)
//  2. maxAge'den eski dizinler (yarım kalmış upload)
func sweepOrphanTmp(tmpRoot string, store *storage.Store, maxAge time.Duration) {
	entries, err := os.ReadDir(tmpRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Sadece geçerli paylaşım token'ı formatındaki dizinleri ele al.
		// session gibi token-olmayan dizinler asla silinmez (session volume'da saklanabilir).
		if !core.ValidShareToken(e.Name()) {
			continue
		}
		p := filepath.Join(tmpRoot, e.Name())
		remove := false
		// DB'de kaydı var mı?
		if store != nil {
			f, err := store.GetFileByToken(e.Name())
			if err != nil {
				// DB hatası → dizini silme (aktif upload'un tmp'si yanlışlıkla kaybolmasın)
				continue
			}
			if f == nil {
				remove = true // DB'de yok → kalıntı
			}
		}
		if !remove {
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > maxAge {
				remove = true
			}
		}
		if remove {
			_ = os.RemoveAll(p)
			log.Printf("Sahipsiz geçici dizin temizlendi: %s", e.Name())
		}
	}
}
