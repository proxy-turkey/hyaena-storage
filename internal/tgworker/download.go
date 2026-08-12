package tgworker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/gotd/td/tg"

	"github.com/proxy-turkey/hyaena-storage/internal/storage"
)

// DownloadSegment, parçayı Telegram'dan indirip w'e akışkan yazar.
func (s *Service) DownloadSegment(ctx context.Context, part storage.Part, w io.Writer) error {
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

	var msgs tg.MessagesMessagesClass
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.call(ctx, func() error {
		var e error
		msgs, e = s.api.MessagesGetMessages(ctx, []tg.InputMessageClass{
			&tg.InputMessageID{ID: *part.TelegramMsgID},
		})
		return e
	})
	if err != nil {
		return false, err
	}

	// MessagesMessagesClass interface; concrete tiplere ayrıştır
	var msg *tg.Message
	switch ms := msgs.(type) {
	case *tg.MessagesMessages:
		for _, m := range ms.Messages {
			if mm, ok := m.(*tg.Message); ok {
				msg = mm
				break
			}
		}
	case *tg.MessagesChannelMessages:
		for _, m := range ms.Messages {
			if mm, ok := m.(*tg.Message); ok {
				msg = mm
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
