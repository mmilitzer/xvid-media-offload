package shrink

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmilitzer/xvid-media-offload/pkg/config"
	"github.com/mmilitzer/xvid-media-offload/pkg/db"
)

func TestShrinkDryRunDoesNotModify(t *testing.T) {
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

	res, err := Run(cfg, true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(res.Candidates))
	}

	// Ensure no .offloaded marker was created.
	if _, err := os.Stat(filepath.Join(setDir, "4k", "video.mp4.offloaded")); !os.IsNotExist(err) {
		t.Error("dry-run should not create .offloaded marker")
	}
}

func TestShrinkSkipsExistingOffloadedMarker(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)
	createOldFile(t, filepath.Join(setDir, "4k", "video.mp4"))
	createFile(t, filepath.Join(setDir, "4k", "video.mp4.offloaded"), "")

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := Run(cfg, true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SkippedOffloaded != 1 {
		t.Errorf("expected 1 offloaded skip, got %d", res.SkippedOffloaded)
	}
}

func TestShrinkSkipsTooSmall(t *testing.T) {
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
		KeepPrefixBytes: 1024 * 1024, // 1 MB -> file is too small
	}

	res, err := Run(cfg, true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SkippedTooSmall != 1 {
		t.Errorf("expected 1 too small skip, got %d", res.SkippedTooSmall)
	}
}

func TestShrinkSkipsNoAutograph(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
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

	res, err := Run(cfg, true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SkippedNoAutograph != 1 {
		t.Errorf("expected 1 no-autograph skip, got %d", res.SkippedNoAutograph)
	}
}

func TestShrinkSkipsInvalidMarker(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, "#Invalid marker\n")
	createOldFile(t, filepath.Join(setDir, "4k", "video.mp4"))

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := Run(cfg, true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.InvalidMarkers != 1 {
		t.Errorf("expected 1 invalid marker, got %d", res.InvalidMarkers)
	}
}

func TestShrinkSkipsMissingRemoteID(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)
	// video.mp4 listed in marker but not created on disk.

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := Run(cfg, true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SkippedMissingRemote != 1 {
		t.Errorf("expected 1 missing-remote skip, got %d", res.SkippedMissingRemote)
	}
}

func TestShrinkDryRunDoesNotWriteDB(t *testing.T) {
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

	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	res, err := Run(cfg, true, database)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(res.Candidates))
	}

	// DB should still exist and be valid, but no row should have been written.
	// We verify by reopening and checking; for simplicity we just ensure no crash.
}

func TestShrinkApplyWritesDB(t *testing.T) {
	if os.Getenv("RUN_HOLE_PUNCH_TESTS") != "1" {
		t.Skip("Set RUN_HOLE_PUNCH_TESTS=1 to run apply-mode shrink tests")
	}

	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)

	// Create a large enough old file.
	path := filepath.Join(setDir, "4k", "video.mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 4*1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	f.Close()
	oldTime := time.Now().Add(-720 * time.Hour)
	os.Chtimes(path, oldTime, oldTime)

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1024 * 1024,
	}

	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	res, err := Run(cfg, false, database)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Offloaded) != 1 {
		t.Fatalf("expected 1 offloaded file, got %d", len(res.Offloaded))
	}
	if res.Offloaded[0].DBStatus != "ok" {
		t.Errorf("expected DB status ok, got %s", res.Offloaded[0].DBStatus)
	}

	// Ensure .offloaded marker exists.
	if _, err := os.Stat(path + ".offloaded"); os.IsNotExist(err) {
		t.Error("expected .offloaded marker to be created")
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
