package tgworker

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/gotd/td/telegram/message/unpack"
	"github.com/gotd/td/tg"

	"github.com/proxy-turkey/hyaena-storage/internal/storage"
)

// UploadSegments, parçaları round-robin kanallara Document olarak yükler.
// Her parça ayrı mesaj. Tümü başarılıysa dosya ready, tek hata → failed.
// Bittiğinde geçici dizin temizlenir (sunucuda kalıcı dosya kalmaz).
func (s *Service) UploadSegments(ctx context.Context, fileID int64, tmpDir string, segmentPaths []string, channelIDs []int64, mime string) error {
	if err := s.apiGuard(ctx); err != nil {
		return err
	}
	// tmp'yi her durumda temizle (başarı veya hata)
	defer func() {
		_ = os.RemoveAll(tmpDir)
		log.Printf("Geçici dizin temizlendi: %s", tmpDir)
	}()
	s.progress.Register(fileID, len(segmentPaths))

	var totalSize int64
	for _, p := range segmentPaths {
		if fi, err := os.Stat(p); err == nil {
			totalSize += fi.Size()
		}
	}
	s.progress.SetTotal(fileID, totalSize)
	defer s.progress.Clear(fileID)

	okCount := 0
	for i, path := range segmentPaths {
		part, err := s.db.GetPart(fileID, i)
		if err != nil {
			continue
		}
		var partID int64
		if part != nil {
			partID = part.ID
		}
		ch, err := s.db.GetChannel(channelIDs[i])
		if err != nil || ch == nil {
			log.Printf("Segment %d kanalı yok (ch=%d): %v", i, channelIDs[i], err)
			if partID != 0 {
				_ = s.db.MarkPartFailed(fileID, i, "kanal yok")
			}
			_ = s.db.SetFileFailed(fileID, "kanal yok")
			return nil
		}

		if err := s.uploadOne(ctx, ch, path, i, mime, fileID); err != nil {
			log.Printf("Segment %d yüklenemedi: %v", i, err)
			if partID != 0 {
				_ = s.db.MarkPartFailed(fileID, i, err.Error())
			}
			_ = s.db.SetFileFailed(fileID, err.Error())
			return err
		}
		okCount++
		// canlı ilerleme: done_parts'i her başarılı segmentte artır
		_ = s.db.BumpDoneParts(fileID)

		if i < len(segmentPaths)-1 {
			time.Sleep(time.Duration(s.s.InterMessageSleep * float64(time.Second)))
		}
	}

	if okCount == len(segmentPaths) {
		_ = s.db.UpdateFileStatus(fileID, "ready", nil)
		log.Printf("Dosya %d hazır (%d segment)", fileID, len(segmentPaths))
	}
	return nil
}

// uploadOne, tek bir segmenti hedef kanala yükler ve DB'ye parça kaydı ekler.
func (s *Service) uploadOne(ctx context.Context, ch *storage.Channel, path string, index int, mime string, fileID int64) error {
	size := int64(0)
	if fi, err := os.Stat(path); err == nil {
		size = fi.Size()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 1) dosyayı MTProto'ya yükle
	var up tg.InputFileClass
	if err := s.call(ctx, func() error {
		var e error
		up, e = s.uploader.FromPath(ctx, path)
		return e
	}); err != nil {
		return err
	}

	// 2) kanala Document mesajı gönder (force_document)
	updates, err := s.callCtx(ctx, func(ctx context.Context) (tg.UpdatesClass, error) {
		return s.api.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
			Peer: s.peer(*ch),
			Media: &tg.InputMediaUploadedDocument{
				File:      up,
				MimeType:  mime,
				ForceFile: true,
				Attributes: []tg.DocumentAttributeClass{
					&tg.DocumentAttributeFilename{FileName: filepath.Base(path)},
				},
			},
			Message:  "",
			RandomID: time.Now().UnixNano(),
		})
	})
	if err != nil {
		return err
	}

	msg, ok := extractMessage(updates)
	if !ok {
		return errMsg("mesaj çözümlenemedi")
	}
	doc, err := extractDocument(msg)
	if err != nil {
		return err
	}

	ref := storage.FileRef{
		ID:            doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: doc.FileReference,
		DCID:          doc.DCID,
	}
	blob, err := json.Marshal(ref)
	if err != nil {
		return err
	}

	dc := doc.DCID
	if err := s.db.AddPart(fileID, index, &ch.ID, msgIDPtr(msg.ID), blob, size, &dc); err != nil {
		return err
	}
	s.progress.AddSent(fileID, size)
	return nil
}

// callCtx, değer döndüren FloodWait-aware çağrı.
func (s *Service) callCtx(ctx context.Context, fn func(ctx context.Context) (tg.UpdatesClass, error)) (tg.UpdatesClass, error) {
	var result tg.UpdatesClass
	err := s.call(ctx, func() error {
		var e error
		result, e = fn(ctx)
		return e
	})
	return result, err
}

// extractMessage, Updates yanıtından *tg.Message çıkarır.
// Kanal mesajları UpdateNewChannelMessage olarak döner; unpack.Message ikisini de ele alır.
func extractMessage(updates tg.UpdatesClass) (*tg.Message, bool) {
	msg, err := unpack.Message(updates, nil)
	if err != nil {
		return nil, false
	}
	return msg, true
}

// extractDocument, mesajdan *tg.Document çıkarır.
func extractDocument(msg *tg.Message) (*tg.Document, error) {
	media, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok || media == nil {
		return nil, errMsg("belge medyası bulunamadı")
	}
	doc, ok := media.Document.AsNotEmpty()
	if !ok || doc == nil {
		return nil, errMsg("belge bulunamadı")
	}
	return doc, nil
}

func msgIDPtr(id int) *int { return &id }

func errMsg(s string) error { return &simpleErr{s} }

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }
