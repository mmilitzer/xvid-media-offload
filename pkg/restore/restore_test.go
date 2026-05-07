package restore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmilitzer/xvid-media-offload/pkg/config"
	"github.com/mmilitzer/xvid-media-offload/pkg/credentials"
	"github.com/mmilitzer/xvid-media-offload/pkg/db"
	"github.com/mmilitzer/xvid-media-offload/pkg/scanner"
	"golang.org/x/sys/unix"
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

type sizedMockDownloader struct {
	size int64
}

func (m *sizedMockDownloader) Download(signedURL string, destPath string) error {
	return m.DownloadContext(context.Background(), signedURL, destPath)
}

func (m *sizedMockDownloader) DownloadContext(ctx context.Context, signedURL string, destPath string) error {
	data := make([]byte, m.size)
	for i := range data {
		data[i] = byte(i % 256)
	}
	return os.WriteFile(destPath, data, 0644)
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

func TestRestoreRecreatesMarkerWithSourcePermissions(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)
	path := filepath.Join(setDir, "4k", "video.mp4")
	createFile(t, path, "old content")
	if err := os.Chmod(path, 0750); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-720 * time.Hour)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

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

	fi, err := os.Stat(path + ".offloaded")
	if err != nil {
		t.Fatalf("unexpected error stat marker: %v", err)
	}
	if fi.Mode().Perm() != 0750 {
		t.Errorf("expected marker mode 0750, got %04o", fi.Mode().Perm())
	}
}

func TestRestoreRecreatesMarkerWithExplicitMode(t *testing.T) {
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
		MarkerFileMode:  "0666",
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

	fi, err := os.Stat(path + ".offloaded")
	if err != nil {
		t.Fatalf("unexpected error stat marker: %v", err)
	}
	if fi.Mode().Perm() != 0666 {
		t.Errorf("expected marker mode 0666, got %04o", fi.Mode().Perm())
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

func TestRestoreOneSkipsMissingSparseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mp4")
	// Do NOT create the actual MP4 file on disk.

	job := Job{
		File: scanner.FileInfo{
			Path:       path,
			RemoteID:   "669872d3d3586a56f9a3dfad",
			Autograph:  1,
			MarkerPath: filepath.Join(dir, ".htaccess"),
			Size:       1024,
		},
		Credentials: &credentials.Credentials{ClientID: "id", ClientSecret: "secret"},
	}

	cfg := &config.Config{
		ScanRoots:      []string{dir},
		CandidateGlobs: []string{"**/*.mp4"},
		MarkerFilename: ".htaccess",
		MinimumAge:     config.Duration(1 * time.Hour),
		Verbose:        true,
	}

	_, err := restoreOne(context.Background(), job, cfg, nil, &mockDownloader{})
	if err == nil {
		t.Fatal("expected error when sparse MP4 is missing")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected error to mention missing file, got: %v", err)
	}
}

func TestRestoreUsesDiskOwnershipNotDB(t *testing.T) {
	if os.Getenv("RUN_HOLE_PUNCH_TESTS") != "1" {
		t.Skip("Set RUN_HOLE_PUNCH_TESTS=1 to run apply-mode restore tests")
	}

	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "content")
	markerDir := filepath.Join(scanRoot, "set1")
	createMarker(t, markerDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createCredentialFile(t, scanRoot, "test-client", "dGVzdC1zZWNyZXQ=")

	path := filepath.Join(markerDir, "video.mp4")
	// Create a genuinely sparse file.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1024 * 1024); err != nil {
		t.Fatal(err)
	}
	f.Close()
	// Set old mtime so it passes minimum_age, and a specific mode.
	oldTime := time.Now().Add(-720 * time.Hour)
	os.Chtimes(path, oldTime, oldTime)
	os.Chmod(path, 0750)

	cfg := &config.Config{
		ScanRoots:       []string{scanRoot},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		OwnershipPolicy: config.OwnershipPolicyAllowOwnerMismatch,
		Verbose:         true,
	}

	// Use a mock downloader that writes the expected file size.
	sizedDownloader := &sizedMockDownloader{size: 1024 * 1024}

	res, err := Run(cfg, false, 1, nil, sizedDownloader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Restored) != 1 {
		t.Fatalf("expected 1 restored file, got %d", len(res.Restored))
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error stat restored file: %v", err)
	}
	if fi.Mode().Perm() != 0750 {
		t.Errorf("expected restored mode 0750 (from disk), got %04o", fi.Mode().Perm())
	}
}

// TestRestoreOnePreservesModeFromDiskWhenJobEmpty proves that even when a
// Job is constructed without explicit OrigUID/OrigGID/OrigMode (as the daemon
// does), restoreOne still preserves the sparse file's mode.
func TestRestoreOnePreservesModeFromDiskWhenJobEmpty(t *testing.T) {
	if os.Getenv("RUN_HOLE_PUNCH_TESTS") != "1" {
		t.Skip("Set RUN_HOLE_PUNCH_TESTS=1 to run apply-mode restore tests")
	}

	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "content")
	markerDir := filepath.Join(scanRoot, "set1")
	createMarker(t, markerDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createCredentialFile(t, scanRoot, "test-client", "dGVzdC1zZWNyZXQ=")

	path := filepath.Join(markerDir, "video.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1024 * 1024); err != nil {
		t.Fatal(err)
	}
	f.Close()
	oldTime := time.Now().Add(-720 * time.Hour)
	os.Chtimes(path, oldTime, oldTime)
	os.Chmod(path, 0750)

	cfg := &config.Config{
		ScanRoots:       []string{scanRoot},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		OwnershipPolicy: config.OwnershipPolicyAllowOwnerMismatch,
		Verbose:         true,
	}

	job := Job{
		File: scanner.FileInfo{
			Path:       path,
			RemoteID:   "669872d3d3586a56f9a3dfad",
			Autograph:  1,
			MarkerPath: filepath.Join(markerDir, ".htaccess"),
			Size:       1024 * 1024,
		},
		Credentials: &credentials.Credentials{ClientID: "test-client", ClientSecret: "dGVzdC1zZWNyZXQ="},
		// Intentionally leave OrigUID, OrigGID, OrigMode at zero to simulate
		// the daemon path where these fields are not populated.
	}

	_, err = RestoreOne(context.Background(), job, cfg, nil, &sizedMockDownloader{size: 1024 * 1024})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error stat restored file: %v", err)
	}
	if fi.Mode().Perm() != 0750 {
		t.Errorf("expected restored mode 0750 (from disk), got %04o", fi.Mode().Perm())
	}
}

// TestRestoreOnePreservesGroupAndModeUnderRequireDaemonOwner verifies that
// restoreOne preserves the group and mode of a daemon-owned sparse file when
// the ownership_policy is require-daemon-owner.  This is the key case that
// was previously broken: only allow-owner-mismatch attempted chown, so files
// restored under require-daemon-owner or replace policies could lose their
// original group.
func TestRestoreOnePreservesGroupAndModeUnderRequireDaemonOwner(t *testing.T) {
	if os.Getenv("RUN_HOLE_PUNCH_TESTS") != "1" {
		t.Skip("Set RUN_HOLE_PUNCH_TESTS=1 to run apply-mode restore tests")
	}

	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "content")
	markerDir := filepath.Join(scanRoot, "set1")
	createMarker(t, markerDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createCredentialFile(t, scanRoot, "test-client", "dGVzdC1zZWNyZXQ=")

	path := filepath.Join(markerDir, "video.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1024 * 1024); err != nil {
		t.Fatal(err)
	}
	f.Close()
	oldTime := time.Now().Add(-720 * time.Hour)
	os.Chtimes(path, oldTime, oldTime)
	os.Chmod(path, 0750)

	// Capture the current ownership of the sparse file (should be the daemon user).
	var sparseStat unix.Stat_t
	if err := unix.Stat(path, &sparseStat); err != nil {
		t.Fatalf("stat sparse file: %v", err)
	}
	sparseGID := int(sparseStat.Gid)
	sparseUID := int(sparseStat.Uid)

	cfg := &config.Config{
		ScanRoots:       []string{scanRoot},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		OwnershipPolicy: config.OwnershipPolicyRequireDaemonOwner,
		Verbose:         true,
	}

	job := Job{
		File: scanner.FileInfo{
			Path:       path,
			RemoteID:   "669872d3d3586a56f9a3dfad",
			Autograph:  1,
			MarkerPath: filepath.Join(markerDir, ".htaccess"),
			Size:       1024 * 1024,
		},
		Credentials: &credentials.Credentials{ClientID: "test-client", ClientSecret: "dGVzdC1zZWNyZXQ="},
	}

	_, err = RestoreOne(context.Background(), job, cfg, nil, &sizedMockDownloader{size: 1024 * 1024})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify mode is preserved.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat restored file: %v", err)
	}
	if fi.Mode().Perm() != 0750 {
		t.Errorf("expected restored mode 0750, got %04o", fi.Mode().Perm())
	}

	// Verify group is preserved.
	var restoredStat unix.Stat_t
	if err := unix.Stat(path, &restoredStat); err != nil {
		t.Fatalf("stat restored file for ownership: %v", err)
	}
	if int(restoredStat.Gid) != sparseGID {
		t.Errorf("expected restored GID %d (from sparse), got %d", sparseGID, restoredStat.Gid)
	}
	if int(restoredStat.Uid) != sparseUID {
		t.Errorf("expected restored UID %d (from sparse), got %d", sparseUID, restoredStat.Uid)
	}
}

// TestRestoreOnePreservesGroupAndModeUnderReplaceWithDaemonOwnedSparseCopy
// verifies that restoreOne preserves group and mode under the
// replace-with-daemon-owned-sparse-copy policy.
func TestRestoreOnePreservesGroupAndModeUnderReplaceWithDaemonOwnedSparseCopy(t *testing.T) {
	if os.Getenv("RUN_HOLE_PUNCH_TESTS") != "1" {
		t.Skip("Set RUN_HOLE_PUNCH_TESTS=1 to run apply-mode restore tests")
	}

	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "content")
	markerDir := filepath.Join(scanRoot, "set1")
	createMarker(t, markerDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createCredentialFile(t, scanRoot, "test-client", "dGVzdC1zZWNyZXQ=")

	path := filepath.Join(markerDir, "video.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1024 * 1024); err != nil {
		t.Fatal(err)
	}
	f.Close()
	oldTime := time.Now().Add(-720 * time.Hour)
	os.Chtimes(path, oldTime, oldTime)
	os.Chmod(path, 0640)

	var sparseStat unix.Stat_t
	if err := unix.Stat(path, &sparseStat); err != nil {
		t.Fatalf("stat sparse file: %v", err)
	}
	sparseGID := int(sparseStat.Gid)
	sparseUID := int(sparseStat.Uid)

	cfg := &config.Config{
		ScanRoots:       []string{scanRoot},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		OwnershipPolicy: config.OwnershipPolicyReplaceWithDaemonOwnedSparse,
		Verbose:         true,
	}

	job := Job{
		File: scanner.FileInfo{
			Path:       path,
			RemoteID:   "669872d3d3586a56f9a3dfad",
			Autograph:  1,
			MarkerPath: filepath.Join(markerDir, ".htaccess"),
			Size:       1024 * 1024,
		},
		Credentials: &credentials.Credentials{ClientID: "test-client", ClientSecret: "dGVzdC1zZWNyZXQ="},
	}

	_, err = RestoreOne(context.Background(), job, cfg, nil, &sizedMockDownloader{size: 1024 * 1024})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat restored file: %v", err)
	}
	if fi.Mode().Perm() != 0640 {
		t.Errorf("expected restored mode 0640, got %04o", fi.Mode().Perm())
	}

	var restoredStat unix.Stat_t
	if err := unix.Stat(path, &restoredStat); err != nil {
		t.Fatalf("stat restored file for ownership: %v", err)
	}
	if int(restoredStat.Gid) != sparseGID {
		t.Errorf("expected restored GID %d (from sparse), got %d", sparseGID, restoredStat.Gid)
	}
	if int(restoredStat.Uid) != sparseUID {
		t.Errorf("expected restored UID %d (from sparse), got %d", sparseUID, restoredStat.Uid)
	}
}

// TestRestoreOneSplitChownUnderAllowOwnerMismatch verifies that under
// allow-owner-mismatch mode the restore uses split chown (group first,
// then owner) so that partial preservation is possible.  When running as
// a non-root user, group changes may succeed even if owner changes fail,
// so the restored file should retain the daemon's uid but the original
// gid if the chgrp succeeds.
func TestRestoreOneSplitChownUnderAllowOwnerMismatch(t *testing.T) {
	if os.Getenv("RUN_HOLE_PUNCH_TESTS") != "1" {
		t.Skip("Set RUN_HOLE_PUNCH_TESTS=1 to run apply-mode restore tests")
	}

	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "content")
	markerDir := filepath.Join(scanRoot, "set1")
	createMarker(t, markerDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createCredentialFile(t, scanRoot, "test-client", "dGVzdC1zZWNyZXQ=")

	path := filepath.Join(markerDir, "video.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1024 * 1024); err != nil {
		t.Fatal(err)
	}
	f.Close()
	oldTime := time.Now().Add(-720 * time.Hour)
	os.Chtimes(path, oldTime, oldTime)
	os.Chmod(path, 0750)

	var sparseStat unix.Stat_t
	if err := unix.Stat(path, &sparseStat); err != nil {
		t.Fatalf("stat sparse file: %v", err)
	}
	sparseGID := int(sparseStat.Gid)
	sparseUID := int(sparseStat.Uid)

	cfg := &config.Config{
		ScanRoots:       []string{scanRoot},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		OwnershipPolicy: config.OwnershipPolicyAllowOwnerMismatch,
		Verbose:         true,
	}

	job := Job{
		File: scanner.FileInfo{
			Path:       path,
			RemoteID:   "669872d3d3586a56f9a3dfad",
			Autograph:  1,
			MarkerPath: filepath.Join(markerDir, ".htaccess"),
			Size:       1024 * 1024,
		},
		Credentials: &credentials.Credentials{ClientID: "test-client", ClientSecret: "dGVzdC1zZWNyZXQ="},
	}

	_, err = RestoreOne(context.Background(), job, cfg, nil, &sizedMockDownloader{size: 1024 * 1024})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Mode must always be preserved regardless of ownership policy.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat restored file: %v", err)
	}
	if fi.Mode().Perm() != 0750 {
		t.Errorf("expected restored mode 0750, got %04o", fi.Mode().Perm())
	}

	// In allow-owner-mismatch mode, the file should be chowned to the
	// original uid/gid if possible.  Since the test creates the file as
	// the current user, the uid and gid should match after restore.
	var restoredStat unix.Stat_t
	if err := unix.Stat(path, &restoredStat); err != nil {
		t.Fatalf("stat restored file for ownership: %v", err)
	}
	if int(restoredStat.Gid) != sparseGID {
		t.Errorf("expected restored GID %d (from sparse), got %d", sparseGID, restoredStat.Gid)
	}
	if int(restoredStat.Uid) != sparseUID {
		t.Errorf("expected restored UID %d (from sparse), got %d", sparseUID, restoredStat.Uid)
	}
}
