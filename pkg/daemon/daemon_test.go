package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmilitzer/xvid-media-offload/pkg/config"
	"github.com/mmilitzer/xvid-media-offload/pkg/db"
	"github.com/mmilitzer/xvid-media-offload/pkg/shrink"
	"github.com/mmilitzer/xvid-media-offload/pkg/sparse"
)

func TestEnqueueRestoreDedup(t *testing.T) {
	cfg := &config.Config{
		ScanRoots:       []string{"/tmp"},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	d := NewDaemon(cfg, false, nil, nil)

	job := restoreJob{Path: "/media/video.mp4", RemoteID: "abc123"}

	if !d.enqueueRestore(job) {
		t.Fatal("expected first enqueue to succeed")
	}
	if d.enqueueRestore(job) {
		t.Fatal("expected duplicate enqueue to fail")
	}

	// Simulate worker completion.
	d.inFlightMu.Lock()
	delete(d.inFlight, job.Path)
	d.inFlightMu.Unlock()

	if !d.enqueueRestore(job) {
		t.Fatal("expected re-enqueue after completion to succeed")
	}
}

func TestResolveRestoreJobFromMarker(t *testing.T) {
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

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	d := NewDaemon(cfg, false, nil, nil)
	job, ok := d.resolveRestoreJob(path)
	if !ok {
		t.Fatal("expected resolve to succeed")
	}
	if job.Path != path {
		t.Errorf("unexpected path: %s", job.Path)
	}
	if job.RemoteID != "669872d3d3586a56f9a3dfad" {
		t.Errorf("unexpected remote id: %s", job.RemoteID)
	}
	if job.Autograph != 1 {
		t.Errorf("unexpected autograph: %d", job.Autograph)
	}
}

func TestResolveRestoreJobFallbackToDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mp4")
	createOldFile(t, path)

	// No marker file.

	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	if err := database.UpsertOffloadedFile(path, "db-remote-id", 0, 100, time.Now()); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	d := NewDaemon(cfg, false, database, nil)
	job, ok := d.resolveRestoreJob(path)
	if !ok {
		t.Fatal("expected resolve to succeed via DB fallback")
	}
	if job.RemoteID != "db-remote-id" {
		t.Errorf("unexpected remote id: %s", job.RemoteID)
	}
	if job.Autograph != 0 {
		t.Errorf("unexpected autograph: %d", job.Autograph)
	}
}

func TestReconcileMissingFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	path := filepath.Join(dir, "missing.mp4")
	if err := database.UpsertOffloadedFile(path, "remote-id", 1, 100, time.Now()); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	d := NewDaemon(cfg, false, database, nil)
	rec := db.OffloadedRecord{LocalPath: path, RemoteFileID: "remote-id", Autograph: 1, OriginalSize: 100}
	d.reconcileRecord(rec)

	_, _, _, _, err = database.GetOffloadedFile(path)
	if err == nil {
		t.Error("expected DB record to be removed for missing file")
	}
}

func TestReconcileRestoredFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	path := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(path, []byte("full content"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := database.UpsertOffloadedFile(path, "remote-id", 1, 12, time.Now()); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	d := NewDaemon(cfg, false, database, nil)
	rec := db.OffloadedRecord{LocalPath: path, RemoteFileID: "remote-id", Autograph: 1, OriginalSize: 12}
	d.reconcileRecord(rec)

	_, _, _, _, err = database.GetOffloadedFile(path)
	if err == nil {
		t.Error("expected DB record to be removed for restored file")
	}
}

func TestReconcileStillOffloaded(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	path := filepath.Join(dir, "video.mp4")
	createSparseFile(t, path)

	// Create .offloaded marker.
	if err := os.WriteFile(path+".offloaded", []byte("offloaded"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := database.UpsertOffloadedFile(path, "remote-id", 1, 100, time.Now()); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	d := NewDaemon(cfg, false, database, nil)
	rec := db.OffloadedRecord{LocalPath: path, RemoteFileID: "remote-id", Autograph: 1, OriginalSize: 100}
	d.reconcileRecord(rec)

	_, _, _, _, err = database.GetOffloadedFile(path)
	if err != nil {
		t.Error("expected DB record to remain for still-offloaded file")
	}
}

func TestReconcileDBOnlyRecovery(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	path := filepath.Join(dir, "video.mp4")
	createSparseFile(t, path)

	// No .offloaded marker, no .htaccess marker.

	if err := database.UpsertOffloadedFile(path, "remote-id", 1, 100, time.Now()); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	d := NewDaemon(cfg, false, database, nil)
	rec := db.OffloadedRecord{LocalPath: path, RemoteFileID: "remote-id", Autograph: 1, OriginalSize: 100}
	d.reconcileRecord(rec)

	// Should be enqueued; verify in-flight.
	d.inFlightMu.Lock()
	inFlight := d.inFlight[path]
	d.inFlightMu.Unlock()
	if !inFlight {
		t.Error("expected DB-only recovery to enqueue restore job")
	}
}

func TestReconcileDryRun(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	path := filepath.Join(dir, "missing.mp4")
	if err := database.UpsertOffloadedFile(path, "remote-id", 1, 100, time.Now()); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	d := NewDaemon(cfg, true, database, nil)
	rec := db.OffloadedRecord{LocalPath: path, RemoteFileID: "remote-id", Autograph: 1, OriginalSize: 100}
	d.reconcileRecord(rec)

	_, _, _, _, err = database.GetOffloadedFile(path)
	if err != nil {
		t.Error("expected DB record to remain in dry-run mode")
	}
}

func TestDaemonStartStop(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	d := NewDaemon(cfg, false, nil, nil)

	done := make(chan error, 1)
	go func() {
		done <- d.Start()
	}()

	// Give it a moment to start.
	time.Sleep(100 * time.Millisecond)
	d.Stop()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error from Start: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop in time")
	}
}

func TestCombinedScanMinimumAgeOnlyForShrink(t *testing.T) {
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
		MinimumAge:      config.Duration(720 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	// For restore, make the file appear sparse.
	// We can't easily make a real sparse file in tests without hole punching,
	// so we test via scanner.ScanForRestore which uses sparse.IsSparse.
	// Since the file is tiny and not sparse, ScanForRestore won't find it.
	// This test verifies that shrink.Run in dry-run finds 0 candidates because of minimum_age.
	res, err := shrink.Run(cfg, true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("expected 0 shrink candidates for young file, got %d", len(res.Candidates))
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

func createSparseFile(t *testing.T, path string) {
	t.Helper()
	createFile(t, path, "sparse content")
	// Create a file that appears sparse by making it large with minimal content.
	// sparse.IsSparse uses allocated blocks * 512 < 0.8 * size.
	// For a tiny file, it won't appear sparse. We need to actually punch a hole.
	// On Linux, we can use fallocate to create a sparse file.
	// But in CI this may not be supported. Instead, we create a larger file
	// and try to punch a hole if supported.
	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Write 1 byte at offset 1024*1024 to create a 1MB file with 1 block allocated.
	if _, err := f.WriteAt([]byte("x"), 1024*1024); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Verify it appears sparse.
	isSparse, err := sparse.IsSparse(path)
	if err != nil {
		t.Fatalf("sparse check failed: %v", err)
	}
	if !isSparse {
		// If filesystem doesn't support sparse detection, skip.
		t.Skip("filesystem does not support sparse file detection")
	}
}
