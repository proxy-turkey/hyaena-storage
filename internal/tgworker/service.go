// Package tgworker, GoMTProto (gotd/td) ile Telegram entegrasyonunu sağlar.
// Python app/telegram_worker.py karşılığı.
package tgworker

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/proxy-turkey/hyaena-storage/internal/config"
	"github.com/proxy-turkey/hyaena-storage/internal/storage"
)

// Service, Telegram istemcisini sarar ve HTTP katmanına hazır arayüz sunar.
type Service struct {
	s  *config.Settings
	db *storage.Store

	client   *telegram.Client
	api      *tg.Client
	sender   *message.Sender
	uploader *uploader.Uploader
	dl       *downloader.Downloader

	mu        sync.Mutex // Telegram mutating çağrıları serileştirir
	runCtx    context.Context
	readyCh   chan struct{}
	readyOnce sync.Once
	loginErr  error

	// pendingCode, LoginOnce(LoginArgs) ile verilen tek seferlik kod.
	pendingCode string

	entityCache map[int64]*tg.InputPeerChannel
	progress    *ProgressStore
}

// New, Telegram worker'ı oluşturur.
func New(s *config.Settings, db *storage.Store) *Service {
	return &Service{
		s:           s,
		db:          db,
		readyCh:     make(chan struct{}),
		entityCache: map[int64]*tg.InputPeerChannel{},
		progress:    NewProgressStore(),
	}
}

// Ready, worker hazır olduğunda kapanır.
func (s *Service) Ready() <-chan struct{} { return s.readyCh }

// WaitReady, login tamamlanana kadar bekler.
func (s *Service) WaitReady(ctx context.Context) error {
	select {
	case <-s.readyCh:
		if s.loginErr != nil {
			return s.loginErr
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run, Telegram client'ı başlatır. Blokar — goroutine içinde çağrılmalı.
// İlk çalıştırmada interaktif login (phone/code) yapar; sonraki oturumlarda
// kayıtlı session'ı yeniden kullanır.
func (s *Service) Run(ctx context.Context) error {
	client := telegram.NewClient(s.s.TelegramAPIID, s.s.TelegramAPIHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: s.s.SessionFile},
		NoUpdates:      true,
	})
	s.client = client

	err := client.Run(ctx, func(ctx context.Context) error {
		s.runCtx = ctx
		if err := s.loginIfNeeded(ctx); err != nil {
			s.readyOnce.Do(func() {
				s.loginErr = fmt.Errorf("Telegram girişi başarısız: %w", err)
				close(s.readyCh)
			})
			return err
		}
		s.api = client.API()
		s.sender = message.NewSender(s.api)
		s.uploader = uploader.NewUploader(s.api)
		s.dl = downloader.NewDownloader()
		s.readyOnce.Do(func() { close(s.readyCh) })

		me, _ := client.Self(ctx)
		if me != nil {
			log.Printf("Telegram girişi: %s (@%s)", me.FirstName, me.Username)
		}
		<-ctx.Done()
		return nil
	})
	return err
}

// GetMe, oturum bilgisini döndürür.
func (s *Service) GetMe(ctx context.Context) (map[string]string, error) {
	me, err := s.client.Self(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{"first_name": me.FirstName, "username": me.Username}, nil
}

// apiGuard, client hazır mı kontrol eder.
func (s *Service) apiGuard(ctx context.Context) error {
	select {
	case <-s.readyCh:
		if s.loginErr != nil {
			return s.loginErr
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if s.api == nil {
		return fmt.Errorf("Telegram API hazır değil")
	}
	return nil
}

// call, FloodWait-aware retry yapar (Python _tg_call karşılığı, inteded semantik).
func (s *Service) call(ctx context.Context, fn func() error) error {
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		wait, ok := tgerr.AsFloodWait(err)
		if ok {
			wait += 5 * time.Second
			if attempt >= 2 { // max 3 deneme (0,1,2)
				return err
			}
			log.Printf("FloodWait %s (%d/3): bekleniyor", wait, attempt+1)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		return err
	}
}

// peer, kanal kaydından InputPeerChannel kurar (MessagesSendMedia için).
func (s *Service) peer(ch storage.Channel) *tg.InputPeerChannel {
	return &tg.InputPeerChannel{ChannelID: ch.TelegramID, AccessHash: ch.AccessHash}
}

// inputChannel, kanal kaydından InputChannel kurar (ChannelsDeleteMessages için).
func (s *Service) inputChannel(ch storage.Channel) *tg.InputChannel {
	return &tg.InputChannel{ChannelID: ch.TelegramID, AccessHash: ch.AccessHash}
}
