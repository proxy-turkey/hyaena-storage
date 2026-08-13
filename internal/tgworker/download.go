package tgworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/proxy-turkey/hyaena-storage/internal/storage"
)

// DownloadSegment, parçayı Telegram'dan indirip w'e akışkan yazar.
// FILE_REFERENCE_EXPIRED hatası alırsa parçayı OTOMATİK resync edip yeniden
// dener — kullanıcı "dosya indirilemiyor" hatası görmeden çözülür.
func (s *Service) DownloadSegment(ctx context.Context, part storage.Part, w io.Writer) error {
	err := s.downloadOnce(ctx, part, w)
	if err == nil {
		return nil
	}
	if !isFileRefExpired(err) {
		return err
	}
	// Reference süresi dolmuş → otomatik resync + tekrar dene (en fazla 1 kez)
	log.Printf("FILE_REFERENCE_EXPIRED (part=%d), otomatik resync yapılıyor", part.ID)
	if ok, _ := s.ResyncPart(ctx, part.ID); !ok {
		return err
	}
	fresh, gerr := s.db.GetPartByID(part.ID)
	if gerr != nil || fresh == nil {
		return err
	}
	return s.downloadOnce(ctx, *fresh, w)
}

// downloadOnce, parçayı blob'undan çözüp Telegram'dan indirir.
func (s *Service) downloadOnce(ctx context.Context, part storage.Part, w io.Writer) error {
	if err := s.apiGuard(ctx); err != nil {
		return err
	}
	var ref storage.FileRef
	if len(part.FileIDBlob) == 0 {
		return fmt.Errorf("parça blob'u boş (resync gerekli)")
	}
	if err := json.Unmarshal(part.FileIDBlob, &ref); err != nil {
		return fmt.Errorf("bozuk file_id blob'u (resync gerekli): %w", err)
	}
	loc := &tg.InputDocumentFileLocation{
		ID:            ref.ID,
		AccessHash:    ref.AccessHash,
		FileReference: ref.FileReference,
		ThumbSize:     "",
	}
	_, err := s.dl.Download(s.api, loc).WithThreads(4).Stream(ctx, w)
	return err
}

// isFileRefExpired, hatanın FILE_REFERENCE_EXPIRED (veya benzeri) olup
// olmadığını döndürür. gotd tgerr ile kod çözülür; bazı durumlarda raw
// mesajda "FILE_REFERENCE" geçer.
func isFileRefExpired(err error) bool {
	if err == nil {
		return false
	}
	var rpcErr *tgerr.Error
	if errors.As(err, &rpcErr) {
		code := rpcErr.Code
		msg := rpcErr.Message
		if code == 400 && (msg == "FILE_REFERENCE_EXPIRED" || msg == "FILE_REFERENCE_" || msg == "FILE_REFERENCE_EXPIRED_2" ||
			msg == "FILE_PART_1" || msg == "FILE_PART_2" || msg == "FILE_PART_MISSING") {
			return true
		}
		// FILE_REFERENCE prefix'i içeren mesajlar
		if code == 400 && len(msg) >= 5 && msg[:5] == "FILE_" {
			return true
		}
	}
	return false
}

// DeleteSegmentMessage, parçanın Telegram mesajını siler.
// NOT: Broadcast kanal mesajları yalnızca ChannelsDeleteMessagesRequest ile silinir.
func (s *Service) DeleteSegmentMessage(ctx context.Context, part storage.Part) error {
	if err := s.apiGuard(ctx); err != nil {
		return err
	}
	if part.ChannelID == nil || part.TelegramMsgID == nil {
		return nil
	}
	ch, err := s.db.GetChannel(*part.ChannelID)
	if err != nil || ch == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.call(ctx, func() error {
		_, err := s.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
			Channel: s.inputChannel(*ch),
			ID:      []int{*part.TelegramMsgID},
		})
		return err
	})
}

// ResyncPart, bozuk blob'u mesaj id'sinden yeniden çözer.
func (s *Service) ResyncPart(ctx context.Context, partID int64) (bool, error) {
	if err := s.apiGuard(ctx); err != nil {
		return false, err
	}
	part, err := s.db.GetPartByID(partID)
	if err != nil || part == nil {
		return false, nil
	}
	if part.TelegramMsgID == nil || part.ChannelID == nil {
		return false, nil
	}
	ch, err := s.db.GetChannel(*part.ChannelID)
	if err != nil || ch == nil {
		return false, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Kanal mesajları MessagesGetMessages ile getirilmez; ChannelsGetMessages gerekir.
	// (Broadcast kanallarda mesaj silme için de ChannelsDeleteMessages şart — aynı kural.)
	var msgs tg.MessagesMessagesClass
	err = s.call(ctx, func() error {
		var e error
		msgs, e = s.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: s.inputChannel(*ch),
			ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: *part.TelegramMsgID}},
		})
		return e
	})
	if err != nil {
		return false, err
	}

	var msg *tg.Message
	switch m := msgs.(type) {
	case *tg.MessagesChannelMessages:
		for _, mm := range m.Messages {
			if t, ok := mm.(*tg.Message); ok {
				msg = t
				break
			}
		}
	}
	if msg == nil || msg.Media == nil {
		return false, nil
	}
	doc, err := extractDocument(msg)
	if err != nil {
		return false, nil
	}
	ref := storage.FileRef{
		ID:            doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: doc.FileReference,
		DCID:          doc.DCID,
	}
	blob, err := json.Marshal(ref)
	if err != nil {
		return false, err
	}
	dc := doc.DCID
	if err := s.db.RefreshPartBlob(partID, *part.TelegramMsgID, blob, &dc); err != nil {
		return false, err
	}
	return true, nil
}

// GetUploadStatus, canlı upload ilerlemesini döndürür (yoksa nil).
func (s *Service) GetUploadStatus(fileID int64) map[string]any {
	return s.progress.Get(fileID)
}

// ClearUpload, upload kaydını temizler.
func (s *Service) ClearUpload(fileID int64) {
	s.progress.Clear(fileID)
}
