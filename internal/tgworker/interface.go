package tgworker

import (
	"context"
	"io"

	"tgshare/internal/storage"
)

// Worker, HTTP katmanının Telegram worker'dan beklediği arayüz.
// (httpapi, bu arayüzü taklit eden stub'larla test edilebilir.)
type Worker interface {
	// Ready, worker hazır olduğunda kapanır (login tamamlanınca).
	Ready() <-chan struct{}
	// WaitReady, worker hazır olana kadar bekler veya hata döner.
	WaitReady(ctx context.Context) error

	// EnsureFleet, bootstrap (boşsa) + günlük kanalı garanti eder.
	EnsureFleet(ctx context.Context) error
	// EnsureDailyChannel, bugünün kanalı yoksa +1 oluşturur.
	EnsureDailyChannel(ctx context.Context) error

	// UploadSegments, parçaları round-robin kanallara yükler.
	UploadSegments(ctx context.Context, fileID int64, tmpDir string, segmentPaths []string, channelIDs []int64, mime string) error
	// GetUploadStatus, canlı upload ilerlemesini döndürür (yoksa nil).
	GetUploadStatus(fileID int64) map[string]any
	// ClearUpload, upload kaydını temizler.
	ClearUpload(fileID int64)

	// DownloadSegment, parçayı Telegram'dan indirip w'e yazar.
	DownloadSegment(ctx context.Context, part storage.Part, w io.Writer) error
	// DeleteSegmentMessage, parçanın Telegram mesajını siler.
	DeleteSegmentMessage(ctx context.Context, part storage.Part) error
	// ResyncPart, bozuk blob'u mesaj id'sinden tazeler.
	ResyncPart(ctx context.Context, partID int64) (bool, error)

	// GetMe, oturum bilgisini döndürür.
	GetMe(ctx context.Context) (map[string]string, error)
}
