package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/proxy-turkey/hyaena-storage/internal/config"
	"github.com/proxy-turkey/hyaena-storage/internal/storage"
)

// stubWorker, tgworker.Worker arayüzünü taklit eder (Telegram'sız test).
type stubWorker struct {
	ready      chan struct{}
	readyOnce  sync.Once
	segments   []string // upload edilen segment yolları
	uploaded   chan struct{}
	mu         sync.Mutex
	failUpload bool
}

func (s *stubWorker) Ready() <-chan struct{} { return s.ready }
func (s *stubWorker) WaitReady(ctx context.Context) error {
	select {
	case <-s.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *stubWorker) markReady() { s.readyOnce.Do(func() { close(s.ready) }) }

func (s *stubWorker) EnsureFleet(ctx context.Context) error      { return nil }
func (s *stubWorker) EnsureDailyChannel(ctx context.Context) error { return nil }

func (s *stubWorker) UploadSegments(ctx context.Context, fileID int64, tmpDir string, segmentPaths []string, channelIDs []int64, mime string) error {
	s.mu.Lock()
	s.segments = append(s.segments, segmentPaths...)
	s.mu.Unlock()
	if s.failUpload {
		return nil // status failed DB tarafından set edilmez; test için yeterli
	}
	// DB'yi ready yap
	return nil
}

func (s *stubWorker) GetUploadStatus(fileID int64) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uploaded != nil {
		return map[string]any{"part_count": len(s.segments), "total_bytes": 0}
	}
	return nil
}
func (s *stubWorker) ClearUpload(fileID int64) {}

func (s *stubWorker) DownloadSegment(ctx context.Context, part storage.Part, w io.Writer) error {
	if part.FileIDBlob != nil {
		_, err := w.Write(part.FileIDBlob)
		return err
	}
	_, err := w.Write([]byte("segment-content"))
	return err
}
func (s *stubWorker) DeleteSegmentMessage(ctx context.Context, part storage.Part) error { return nil }
func (s *stubWorker) ResyncPart(ctx context.Context, partID int64) (bool, error)        { return true, nil }
func (s *stubWorker) GetMe(ctx context.Context) (map[string]string, error) {
	return map[string]string{"first_name": "Test", "username": "t"}, nil
}

// testDBURL, Supabase DSN'sine izole test şeması ekler.
// Gerçek veritabanı bozulmaz; _test şemasındaki tablolar kullanılır.
func testDBURL() string {
	base := os.Getenv("HYAENA_TEST_DB_URL")
	if base == "" {
		// .env'den oku
		if data, err := os.ReadFile("../../.env"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "DATABASE_URL=") {
					base = strings.TrimSpace(strings.TrimPrefix(line, "DATABASE_URL="))
					break
				}
			}
		}
	}
	if base == "" {
		return ""
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "search_path=tgshare_test"
}

func newTestServer(t *testing.T) (*Server, *stubWorker, http.Handler) {
	t.Helper()
	url := testDBURL()
	if url == "" {
		t.Skip("DATABASE_URL bulunamadı — test atlandı")
	}
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.AdminPassword = "test-pass"
	cfg.DatabaseURL = url
	cfg.TmpRoot = filepath.Join(root, "data", "tmp")

	store, err := storage.Open(cfg.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// statik FS: boş embed benzeri
	staticFS := emptyFS{}

	sw := &stubWorker{ready: make(chan struct{})}
	sw.markReady()

	handler := New(context.Background(), cfg, store, sw, staticFS)
	return &Server{s: cfg, store: store, tw: sw}, sw, handler
}

// emptyFS, statik olmadan index/admin için boş dosya döndürür.
type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, ".html") {
		return fsFile{name: name}, nil
	}
	return nil, os.ErrNotExist
}

type fsFile struct{ name string }

func (fsFile) Stat() (os.FileInfo, error) { return nil, os.ErrNotExist }
func (fsFile) Read([]byte) (int, error)   { return 0, io.EOF }
func (fsFile) Close() error               { return nil }

func doReq(t *testing.T, h http.Handler, method, path string, body io.Reader, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthAndStatic(t *testing.T) {
	_, _, h := newTestServer(t)
	rec := doReq(t, h, "GET", "/health", nil, nil)
	if rec.Code != 200 {
		t.Errorf("health = %d, want 200", rec.Code)
	}
	var hb map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &hb)
	if hb["ok"] != true {
		t.Errorf("health ok = %v", hb["ok"])
	}
}

func TestUploadFlow(t *testing.T) {
	// 1. kanal ekle (yoksa upload başlamaz) — manuel kur
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.AdminPassword = "test-pass"
	cfg.DatabaseURL = testDBURL()
	cfg.TmpRoot = filepath.Join(root, "data", "tmp")
	store, _ := storage.Open(cfg.DBFile())
	defer store.Close()
	store.CreateChannel(-1001, 111, "Kanal", "2026-08-11")
	sw2 := &stubWorker{ready: make(chan struct{})}
	sw2.markReady()
	handler := New(context.Background(), cfg, store, sw2, emptyFS{})

	// upload start
	body := `{"name":"test.bin","size":100,"mime":"application/octet-stream"}`
	rec := doReq(t, handler, "POST", "/api/upload/start", strings.NewReader(body), nil)
	if rec.Code != 200 {
		t.Fatalf("start = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var startResp struct {
		Token      string `json:"token"`
		PartCount  int    `json:"part_count"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &startResp)
	if startResp.Token == "" || startResp.PartCount != 1 {
		t.Fatalf("start yanıtı yanlış: %s", rec.Body.String())
	}

	// parça yükle
	rec = doReq(t, handler, "POST", "/api/upload/"+startResp.Token+"/parts/0", strings.NewReader("0123456789"), nil)
	if rec.Code != 200 {
		t.Fatalf("part = %d: %s", rec.Code, rec.Body.String())
	}
	var partResp struct {
		Received int64 `json:"received"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &partResp)
	if partResp.Received != 10 {
		t.Errorf("received = %d, want 10", partResp.Received)
	}

	// finish
	rec = doReq(t, handler, "POST", "/api/upload/"+startResp.Token+"/finish", nil, nil)
	if rec.Code != 202 {
		t.Fatalf("finish = %d: %s", rec.Code, rec.Body.String())
	}

	// status
	rec = doReq(t, handler, "GET", "/api/upload/"+startResp.Token+"/status", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var stResp struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &stResp)
	if stResp.Status != "uploading" {
		t.Errorf("status = %s, want uploading", stResp.Status)
	}
}

func TestDownloadErrors(t *testing.T) {
	_, sw, h := newTestServer(t)
	_ = sw
	// geçersiz token
	rec := doReq(t, h, "GET", "/api/download/gecersiztoken", nil, nil)
	if rec.Code != 400 {
		t.Errorf("invalid token = %d, want 400", rec.Code)
	}
	// var olmayan dosya
	rec = doReq(t, h, "GET", "/api/download/abcdefghijklmnopqrstuvwxyz123456", nil, nil)
	if rec.Code != 404 {
		t.Errorf("missing file = %d, want 404", rec.Code)
	}
}

func TestAdminAuth(t *testing.T) {
	_, sw, h := newTestServer(t)
	_ = sw
	// korumalı endpoint, cookie yok → 401
	rec := doReq(t, h, "GET", "/api/admin/summary", nil, nil)
	if rec.Code != 401 {
		t.Errorf("no cookie = %d, want 401", rec.Code)
	}
	// yanlış şifre
	rec = doReq(t, h, "POST", "/api/admin/login", strings.NewReader(`{"password":"yanlis"}`), nil)
	if rec.Code != 401 {
		t.Errorf("yanlış şifre = %d, want 401", rec.Code)
	}
	// doğru şifre → 204 + cookie
	rec = doReq(t, h, "POST", "/api/admin/login", strings.NewReader(`{"password":"test-pass"}`), nil)
	if rec.Code != 204 {
		t.Fatalf("doğru şifre = %d, want 204", rec.Code)
	}
	var cookies []*http.Cookie
	for _, c := range rec.Result().Cookies() {
		cookies = append(cookies, c)
	}
	if len(cookies) == 0 {
		t.Fatal("cookie set edilmedi")
	}
	// cookie ile korumalı endpoint
	rec = doReq(t, h, "GET", "/api/admin/summary", nil, cookies)
	if rec.Code != 200 {
		t.Errorf("cookie ile summary = %d, want 200", rec.Code)
	}
}

func TestLegacyRedirect(t *testing.T) {
	_, _, h := newTestServer(t)
	rec := doReq(t, h, "GET", "/download/tokennnnnnnnnnnnnnnnnnnn/name.bin", nil, nil)
	if rec.Code != 307 {
		t.Errorf("legacy = %d, want 307", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/download/tokennnnnnnnnnnnnnnnnnnn/name.bin" {
		t.Errorf("Location = %q", loc)
	}
}
