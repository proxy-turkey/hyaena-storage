package tgworker

import (
	"context"
	"fmt"
	"log"
	"time"
)

// RunHeartbeat, Telegram bağlantısının canlılığını periyodik olarak yoklar.
//
// Neden var: 2026-10 outage'inde sürecin bellek içi MTProto durumu bozuldu
// (her RPC CONNECTION_LAYER_INVALID); /health "telegram_ready: true" derken
// upload/download çalışmıyordu ve düzelmesi için elle restart gerekti.
// Gözcü art arda maxFailures hata görerse süreci kapatır — systemd
// Restart=always 5 saniyede taze süreç başlatır (kanıtlanmış çözüm).
//
// GetMe ucuz tek RPC'dir (users.getUsers) ve initConnection yolunu da
// exercise eder: layer/init sorunlarında hemen yakalar.
func (s *Service) RunHeartbeat(ctx context.Context, interval time.Duration, maxFailures int) {
	failures := 0
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Login henüz tamamlanmadıysa hata sayma (başlangıç/relogin).
			readyCtx, rc := context.WithTimeout(ctx, 5*time.Second)
			err := s.WaitReady(readyCtx)
			rc()
			if err != nil {
				continue
			}

			probeCtx, pc := context.WithTimeout(ctx, 30*time.Second)
			_, err = s.GetMe(probeCtx)
			pc()
			if err == nil {
				if failures > 0 {
					log.Printf("Telegram heartbeat düzeldi (önceki hatalar: %d)", failures)
				}
				failures = 0
				continue
			}
			failures++
			log.Printf("Telegram heartbeat HATA (%d/%d): %v", failures, maxFailures, err)
			if failures >= maxFailures {
				// Kapanmadan önce sahibi uyar (L2 izleme, B-02): heartbeat kendi
				// kendini iyileştirir ama insan da haberdar olmalı.
				s.NotifyOwnerBestEffort(fmt.Sprintf(
					"⚠️ Hyaena Storage: Telegram bağlantısı %d kez üst üste başarısız (%v). "+
						"Süreç yeniden başlatılıyor; durum tekrarlanırsa müdahale gerekir.",
					failures, err))
				log.Fatalf("Telegram heartbeat: %d üst üste hata — süreç bilinçli olarak kapatılıyor (systemd yeniden başlataracak)", failures)
			}
		}
	}
}
