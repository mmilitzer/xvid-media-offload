package restore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmilitzer/xvid-media-offload/pkg/config"
	"github.com/mmilitzer/xvid-media-offload/pkg/db"
	"github.com/mmilitzer/xvid-media-offload/pkg/scanner"
)

type mockDownloader struct {
	shouldFail bool
	calls      []struct {
		url  string
		dest string
	}
}

func (m *mockDownloader) Download(signedURL string, destPath string) error {
	return m.DownloadContext(context.Background(), signedURL, destPath)
}

func (m *mockDownloader) DownloadContext(ctx context.Context, signedURL string, destPath string) error {
	m.calls = append(m.calls, struct {
		url  string
		dest string
	}{signedURL, destPath})
	if m.shouldFail {
		return fmt.Errorf("mock download failure")
	}
	// Write some content so size checks can pass.
	return os.WriteFile(destPath, []byte("restored content"), 0644)
}

func TestRestoreDryRunDoesNotModify(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)
	createOldFile(t, filepath.Join(setDir, "4k", "video.mp4"))

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	// Override scanner to return a sparse-like file info without real hole punching.
	origScan := scanForRestore
	scanForRestore = func(c *config.Config) (*scanner.ScanResult, error) {
		return &scanner.ScanResult{
			Files: []scanner.FileInfo{
				{
					Path:               filepath.Join(setDir, "4k", "video.mp4"),
					RelPath:            "set1/4k/video.mp4",
					Size:               100,
					RemoteID:           "669872d3d3586a56f9a3dfad",
					Autograph:          1,
					MarkerPath:         filepath.Join(setDir, ".htaccess"),
					ScanRoot:           dir,
					IsSparse:           true,
					HasOffloadedMarker: false,
				},
			},
		}, nil
	}
	defer func() { scanForRestore = origScan }()

	downloader := &mockDownloader{}
	res, err := Run(cfg, true, 1, nil, downloader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Candidates != 1 {
		t.Errorf("expected 1 candidate, got %d", res.Candidates)
	}
	if res.Queued != 1 {
		t.Errorf("expected 1 queued, got %d", res.Queued)
	}
	if len(downloader.calls) != 0 {
		t.Error("dry-run should not call downloader")
	}
}

func TestRestoreSkipsNoRemoteID(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
`)
	createOldFile(t, filepath.Join(setDir, "4k", "video.mp4"))

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	origScan := scanForRestore
	scanForRestore = func(c *config.Config) (*scanner.ScanResult, error) {
		return &scanner.ScanResult{
			Files: []scanner.FileInfo{
				{
					Path:               filepath.Join(setDir, "4k", "video.mp4"),
					RelPath:            "set1/4k/video.mp4",
					Size:               100,
					RemoteID:           "",
					Autograph:          -1,
					MarkerPath:         filepath.Join(setDir, ".htaccess"),
					ScanRoot:           dir,
					IsSparse:           true,
					HasOffloadedMarker: false,
				},
			},
		}, nil
	}
	defer func() { scanForRestore = origScan }()

	res, err := Run(cfg, false, 1, nil, &mockDownloader{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SkippedNoRemoteID != 1 {
		t.Errorf("expected 1 skipped no remote id, got %d", res.SkippedNoRemoteID)
	}
}

func TestRestoreUsesDBFallback(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
`)
	path := filepath.Join(setDir, "4k", "video.mp4")
	createOldFile(t, path)

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	origScan := scanForRestore
	scanForRestore = func(c *config.Config) (*scanner.ScanResult, error) {
		return &scanner.ScanResult{
			Files: []scanner.FileInfo{
				{
					Path:               path,
					RelPath:            "set1/4k/video.mp4",
					Size:               100,
					RemoteID:           "",
					Autograph:          -1,
					MarkerPath:         filepath.Join(setDir, ".htaccess"),
					ScanRoot:           dir,
					IsSparse:           true,
					HasOffloadedMarker: false,
				},
			},
		}, nil
	}
	defer func() { scanForRestore = origScan }()

	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	// Insert fallback record.
	if err := database.UpsertOffloadedFile(path, "db-remote-id", 1, 100, time.Now()); err != nil {
		t.Fatal(err)
	}

	res, err := Run(cfg, true, 1, database, &mockDownloader{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SkippedNoRemoteID != 0 {
		t.Errorf("expected 0 skipped no remote id when DB fallback exists, got %d", res.SkippedNoRemoteID)
	}
	if res.Queued != 1 {
		t.Errorf("expected 1 queued, got %d", res.Queued)
	}
}

func TestRestoreWorkerTempFileRename(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)
	path := filepath.Join(setDir, "4k", "video.mp4")
	createOldFile(t, path)

	// Create credential file.
	createCredentialFile(t, dir, "test-client", "dGVzdC1zZWNyZXQ=")

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	origScan := scanForRestore
	scanForRestore = func(c *config.Config) (*scanner.ScanResult, error) {
		return &scanner.ScanResult{
			Files: []scanner.FileInfo{
				{
					Path:               path,
					RelPath:            "set1/4k/video.mp4",
					Size:               16,
					RemoteID:           "669872d3d3586a56f9a3dfad",
					Autograph:          1,
					MarkerPath:         filepath.Join(setDir, ".htaccess"),
					ScanRoot:           dir,
					IsSparse:           true,
					HasOffloadedMarker: false,
				},
			},
		}, nil
	}
	defer func() { scanForRestore = origScan }()

	origDiskCheck := checkDiskSpace
	checkDiskSpace = func(p string, n int64) (bool, int64, error) {
		return true, 1024 * 1024 * 1024, nil
	}
	defer func() { checkDiskSpace = origDiskCheck }()

	downloader := &mockDownloader{}
	res, err := Run(cfg, false, 1, nil, downloader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Restored) != 1 {
		t.Fatalf("expected 1 restored, got %d", len(res.Restored))
	}
	if res.Restored[0].Path != path {
		t.Errorf("unexpected restored path: %s", res.Restored[0].Path)
	}
	if len(downloader.calls) != 1 {
		t.Fatalf("expected 1 download call, got %d", len(downloader.calls))
	}
	if !filepath.IsAbs(downloader.calls[0].dest) {
		t.Error("expected absolute temp path")
	}
	if filepath.Base(downloader.calls[0].dest) == filepath.Base(path) {
		t.Error("downloader should write to temp file, not target directly")
	}

	// Ensure no leftover temp files.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestRestoreRecreatesMarkerWhenNoSpace(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)
	path := filepath.Join(setDir, "4k", "video.mp4")
	createOldFile(t, path)

	createCredentialFile(t, dir, "test-client", "dGVzdC1zZWNyZXQ=")

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	origScan := scanForRestore
	scanForRestore = func(c *config.Config) (*scanner.ScanResult, error) {
		return &scanner.ScanResult{
			Files: []scanner.FileInfo{
				{
					Path:               path,
					RelPath:            "set1/4k/video.mp4",
					Size:               100,
					RemoteID:           "669872d3d3586a56f9a3dfad",
					Autograph:          1,
					MarkerPath:         filepath.Join(setDir, ".htaccess"),
					ScanRoot:           dir,
					IsSparse:           true,
					HasOffloadedMarker: false,
				},
			},
		}, nil
	}
	defer func() { scanForRestore = origScan }()

	origDiskCheck := checkDiskSpace
	checkDiskSpace = func(p string, n int64) (bool, int64, error) {
		return false, 0, nil
	}
	defer func() { checkDiskSpace = origDiskCheck }()

	res, err := Run(cfg, false, 1, nil, &mockDownloader{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SkippedNoSpace != 1 {
		t.Errorf("expected 1 skipped no space, got %d", res.SkippedNoSpace)
	}

	// Marker should be recreated.
	if _, err := os.Stat(path + ".offloaded"); os.IsNotExist(err) {
		t.Error("expected .offloaded marker to be recreated")
	}
}

func TestRestoreMinimumAgeIgnored(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)
	path := filepath.Join(setDir, "4k", "video.mp4")
	// Create a brand new file (very young).
	createFile(t, path, "new content")

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(720 * time.Hour), // very old requirement
		KeepPrefixBytes: 1,
	}

	origScan := scanForRestore
	scanForRestore = func(c *config.Config) (*scanner.ScanResult, error) {
		return &scanner.ScanResult{
			Files: []scanner.FileInfo{
				{
					Path:               path,
					RelPath:            "set1/4k/video.mp4",
					Size:               100,
					RemoteID:           "669872d3d3586a56f9a3dfad",
					Autograph:          1,
					MarkerPath:         filepath.Join(setDir, ".htaccess"),
					ScanRoot:           dir,
					IsSparse:           true,
					HasOffloadedMarker: false,
				},
			},
		}, nil
	}
	defer func() { scanForRestore = origScan }()

	res, err := Run(cfg, true, 1, nil, &mockDownloader{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Candidates != 1 {
		t.Errorf("expected 1 candidate (minimum age ignored), got %d", res.Candidates)
	}
}

func TestRestoreCleansStaleTempBeforeDiskCheck(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)
	path := filepath.Join(setDir, "4k", "video.mp4")
	createOldFile(t, path)

	createCredentialFile(t, dir, "test-client", "dGVzdC1zZWNyZXQ=")

	// Create a stale large temp file that would make disk check fail if not cleaned.
	staleTemp := path + ".restore.stale-uuid.tmp"
	if err := os.WriteFile(staleTemp, make([]byte, 1024*1024), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	origScan := scanForRestore
	scanForRestore = func(c *config.Config) (*scanner.ScanResult, error) {
		return &scanner.ScanResult{
			Files: []scanner.FileInfo{
				{
					Path:               path,
					RelPath:            "set1/4k/video.mp4",
					Size:               16,
					RemoteID:           "669872d3d3586a56f9a3dfad",
					Autograph:          1,
					MarkerPath:         filepath.Join(setDir, ".htaccess"),
					ScanRoot:           dir,
					IsSparse:           true,
					HasOffloadedMarker: false,
				},
			},
		}, nil
	}
	defer func() { scanForRestore = origScan }()

	// Mock disk check to fail if the stale temp still exists.
	origDiskCheck := checkDiskSpace
	checkDiskSpace = func(p string, n int64) (bool, int64, error) {
		if _, err := os.Stat(staleTemp); err == nil {
			return false, 0, nil
		}
		return true, 1024 * 1024 * 1024, nil
	}
	defer func() { checkDiskSpace = origDiskCheck }()

	downloader := &mockDownloader{}
	res, err := Run(cfg, false, 1, nil, downloader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Restored) != 1 {
		t.Fatalf("expected 1 restored, got %d", len(res.Restored))
	}
	if _, err := os.Stat(staleTemp); !os.IsNotExist(err) {
		t.Error("expected stale temp file to be cleaned before disk check")
	}
}

func createMarker(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".htaccess")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func createFile(t *testing.T, path, content string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func createOldFile(t *testing.T, path string) {
	t.Helper()
	createFile(t, path, "old content")
	oldTime := time.Now().Add(-720 * time.Hour)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
}

func createCredentialFile(t *testing.T, dir, clientID, clientSecret string) {
	t.Helper()
	content := fmt.Sprintf("[xvid]\nAPP_CLIENT_ID = \"%s\"\nAPP_CLIENT_SECRET = \"%s\"\n", clientID, clientSecret)
	path := filepath.Join(dir, "cmsinclude.ini.php")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
