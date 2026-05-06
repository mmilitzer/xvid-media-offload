package daemon

import (
	"context"
	"fmt"
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
		LockFile:        filepath.Join(t.TempDir(), "daemon.lock"),
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

// cancellableDownloader blocks until its context is cancelled, then returns the ctx error.
type cancellableDownloader struct {
	started chan struct{}
}

func (c *cancellableDownloader) Download(signedURL string, destPath string) error {
	return c.DownloadContext(context.Background(), signedURL, destPath)
}

func (c *cancellableDownloader) DownloadContext(ctx context.Context, signedURL string, destPath string) error {
	close(c.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestShutdownCancelsActiveRestore(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)
	path := filepath.Join(setDir, "4k", "video.mp4")
	// Create a sparse file so the initial scan enqueues a restore job.
	createSparseFile(t, path)

	createCredentialFile(t, dir, "test-client", "dGVzdC1zZWNyZXQ=")

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
		LockFile:        filepath.Join(t.TempDir(), "daemon.lock"),
	}

	downloader := &cancellableDownloader{started: make(chan struct{})}
	d := NewDaemon(cfg, false, nil, downloader)

	// Start the daemon in background.
	done := make(chan error, 1)
	go func() {
		done <- d.Start()
	}()

	// Wait for the downloader to start (meaning restore is active).
	select {
	case <-downloader.started:
	case <-time.After(2 * time.Second):
		t.Fatal("downloader did not start")
	}

	// Create a temp file to verify cleanup.
	tmpPath := path + ".restore.abc123.tmp"
	if err := os.WriteFile(tmpPath, []byte("partial"), 0644); err != nil {
		t.Fatal(err)
	}

	// Trigger shutdown while restore is in progress.
	d.Stop()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not stop in time")
	}

	// Verify the partial temp file was cleaned up.
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("expected partial temp file to be deleted after shutdown cancellation")
	}
}

func TestEnqueueRestoreNoPanicAfterShutdown(t *testing.T) {
	cfg := &config.Config{
		ScanRoots:       []string{"/tmp"},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
		LockFile:        filepath.Join(t.TempDir(), "daemon.lock"),
	}

	d := NewDaemon(cfg, false, nil, nil)

	// Start and immediately stop to set ctx to cancelled.
	go d.Start()
	time.Sleep(50 * time.Millisecond)
	d.Stop()

	// Give shutdown time to cancel ctx but not necessarily close queue yet.
	time.Sleep(50 * time.Millisecond)

	// Enqueue should not panic; it should return false because ctx is done.
	job := restoreJob{Path: "/media/video.mp4", RemoteID: "abc123"}
	if d.enqueueRestore(job) {
		t.Error("expected enqueue to fail after shutdown")
	}
}

func TestReconcileDBOnlyBlockingWithFullQueue(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	// Create 5 sparse files with DB records but no markers.
	paths := make([]string, 5)
	for i := 0; i < 5; i++ {
		paths[i] = filepath.Join(dir, fmt.Sprintf("video%d.mp4", i))
		createSparseFile(t, paths[i])
		if err := database.UpsertOffloadedFile(paths[i], fmt.Sprintf("remote-id-%d", i), 1, 100, time.Now()); err != nil {
			t.Fatal(err)
		}
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
	// Use a tiny queue so blocking is required.
	d.queue = make(chan restoreJob, 1)

	// Start a slow worker so the queue fills up.
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for job := range d.queue {
			time.Sleep(100 * time.Millisecond)
			d.inFlightMu.Lock()
			delete(d.inFlight, job.Path)
			d.inFlightMu.Unlock()
		}
	}()

	// Reconciliation should block but eventually enqueue all 5 jobs.
	d.reconcileDB()

	// Wait for all jobs to be processed.
	close(d.queue)
	d.wg.Wait()

	// All 5 should have been enqueued and processed.
	for _, p := range paths {
		d.inFlightMu.Lock()
		inFlight := d.inFlight[p]
		d.inFlightMu.Unlock()
		if inFlight {
			t.Errorf("expected job for %s to be processed", p)
		}
	}
}

func TestNewlyOffloadedFilesGetWatchedImmediately(t *testing.T) {
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
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	d := NewDaemon(cfg, false, database, nil)

	// Start inotify manually.
	if err := d.startInotify(); err != nil {
		t.Skipf("inotify not available: %v", err)
	}

	// Run combined scan in apply mode.
	d.combinedScan()

	// Verify the file was offloaded.
	if _, err := os.Stat(path + ".offloaded"); os.IsNotExist(err) {
		t.Fatal("expected .offloaded marker to be created")
	}

	// Verify inotify watch was registered for the parent directory.
	d.watchesMu.Lock()
	_, watched := d.watches[filepath.Dir(path)]
	d.watchesMu.Unlock()
	if !watched {
		t.Error("expected parent dir of newly offloaded file to be watched immediately")
	}

	d.stopInotify()
}

func TestReconcileRestoresOffloadedFileWhenMarkerMissing(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	// Create marker initially so the file gets offloaded.
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)

	path := filepath.Join(setDir, "4k", "video.mp4")
	createSparseFile(t, path)

	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	if err := database.UpsertOffloadedFile(path, "remote-id", 1, 100, time.Now()); err != nil {
		t.Fatal(err)
	}

	// Now delete the main marker so the scan won't find this folder.
	if err := os.Remove(filepath.Join(setDir, ".htaccess")); err != nil {
		t.Fatal(err)
	}

	// Create .offloaded marker to simulate an intentionally offloaded file.
	if err := os.WriteFile(path+".offloaded", []byte("offloaded"), 0644); err != nil {
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
		LockFile:        filepath.Join(t.TempDir(), "daemon.lock"),
	}

	d := NewDaemon(cfg, false, database, nil)

	rec := db.OffloadedRecord{LocalPath: path, RemoteFileID: "remote-id", Autograph: 1, OriginalSize: 100}
	d.reconcileRecord(rec)

	// The .offloaded marker should have been removed.
	if _, err := os.Stat(path + ".offloaded"); !os.IsNotExist(err) {
		t.Error("expected .offloaded marker to be removed when main marker is missing")
	}

	// A restore job should have been enqueued.
	d.inFlightMu.Lock()
	inFlight := d.inFlight[path]
	d.inFlightMu.Unlock()
	if !inFlight {
		t.Error("expected restore job to be enqueued when main marker is missing")
	}
}

func TestReconcileRestoresOffloadedFileWhenMarkerHasNoFileID(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	// Create a marker that is valid but does not contain a file_id for this file.
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/other.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)

	path := filepath.Join(setDir, "4k", "video.mp4")
	createSparseFile(t, path)

	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	if err := database.UpsertOffloadedFile(path, "remote-id", 1, 100, time.Now()); err != nil {
		t.Fatal(err)
	}

	// Create .offloaded marker to simulate an intentionally offloaded file.
	if err := os.WriteFile(path+".offloaded", []byte("offloaded"), 0644); err != nil {
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
		LockFile:        filepath.Join(t.TempDir(), "daemon.lock"),
	}

	d := NewDaemon(cfg, false, database, nil)

	rec := db.OffloadedRecord{LocalPath: path, RemoteFileID: "remote-id", Autograph: 1, OriginalSize: 100}
	d.reconcileRecord(rec)

	// The .offloaded marker should have been removed.
	if _, err := os.Stat(path + ".offloaded"); !os.IsNotExist(err) {
		t.Error("expected .offloaded marker to be removed when main marker no longer contains file id")
	}

	// A restore job should have been enqueued.
	d.inFlightMu.Lock()
	inFlight := d.inFlight[path]
	d.inFlightMu.Unlock()
	if !inFlight {
		t.Error("expected restore job to be enqueued when main marker no longer contains file id")
	}
}

func TestReconcileMarkerLookupInSubdirectory(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	// Marker is in setDir, file is in setDir/4k/.
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)

	path := filepath.Join(setDir, "4k", "video.mp4")
	createSparseFile(t, path)

	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

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

	// The marker lookup should walk upward from setDir/4k/ to setDir/ and find
	// the file id, so this should be treated as Case 4 (marker has file id) and
	// NOT enqueued for DB-only recovery.
	d.reconcileRecord(rec)

	d.inFlightMu.Lock()
	inFlight := d.inFlight[path]
	d.inFlightMu.Unlock()
	if inFlight {
		t.Error("expected reconcile to find marker in parent dir and NOT enqueue DB-only recovery")
	}
}

func TestFindScanRootForPath_Table(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		roots     []string
		wantFound bool
	}{
		{
			name:      "path inside scan root",
			path:      "/site/content/video.mp4",
			roots:     []string{"/site/content"},
			wantFound: true,
		},
		{
			name:      "path outside scan root",
			path:      "/other/video.mp4",
			roots:     []string{"/site/content"},
			wantFound: false,
		},
		{
			name:      "sibling path prefix does not match",
			path:      "/site/content2/video.mp4",
			roots:     []string{"/site/content"},
			wantFound: false,
		},
		{
			name:      "exact scan root path",
			path:      "/site/content",
			roots:     []string{"/site/content"},
			wantFound: true,
		},
		{
			name:      "parent path does not match",
			path:      "/site",
			roots:     []string{"/site/content"},
			wantFound: false,
		},
		{
			name:      "multiple roots second matches",
			path:      "/site/b/video.mp4",
			roots:     []string{"/site/a", "/site/b"},
			wantFound: true,
		},
		{
			name:      "trailing slash in root",
			path:      "/site/content/video.mp4",
			roots:     []string{"/site/content/"},
			wantFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := findScanRootForPath(tt.path, tt.roots)
			if got != tt.wantFound {
				t.Errorf("findScanRootForPath(%q, %v) found = %v, want %v", tt.path, tt.roots, got, tt.wantFound)
			}
		})
	}
}

func TestFindScanRootForPath(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		roots     []string
		wantRoot  string
		wantFound bool
	}{
		{
			name:      "path inside scan root",
			path:      "/site/content/video.mp4",
			roots:     []string{"/site/content"},
			wantRoot:  "/site/content",
			wantFound: true,
		},
		{
			name:      "sibling path prefix does not match",
			path:      "/site/content2/video.mp4",
			roots:     []string{"/site/content"},
			wantRoot:  "",
			wantFound: false,
		},
		{
			name:      "multiple roots second matches",
			path:      "/site/b/video.mp4",
			roots:     []string{"/site/a", "/site/b"},
			wantRoot:  "/site/b",
			wantFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRoot, gotFound := findScanRootForPath(tt.path, tt.roots)
			if gotFound != tt.wantFound {
				t.Errorf("findScanRootForPath(%q, %v) found = %v, want %v", tt.path, tt.roots, gotFound, tt.wantFound)
			}
			if gotFound && gotRoot != tt.wantRoot {
				t.Errorf("findScanRootForPath(%q, %v) root = %q, want %q", tt.path, tt.roots, gotRoot, tt.wantRoot)
			}
		})
	}
}

func TestFindMarkerForPathWithinIgnoresMarkerAboveScanRoot(t *testing.T) {
	dir := t.TempDir()
	// Marker is in the parent of the scan root.
	createMarker(t, dir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)

	scanRoot := filepath.Join(dir, "content")
	if err := os.MkdirAll(scanRoot, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(scanRoot, "video.mp4")
	createOldFile(t, path)

	markerPath, markerDir := findMarkerForPathWithin(path, ".htaccess", scanRoot)
	if markerPath != "" || markerDir != "" {
		t.Errorf("expected no marker found above scan_root, got path=%q dir=%q", markerPath, markerDir)
	}
}

func TestFindMarkerForPathWithinFindsMarkerInsideScanRoot(t *testing.T) {
	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "content")
	createMarker(t, scanRoot, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)

	path := filepath.Join(scanRoot, "video.mp4")
	createOldFile(t, path)

	markerPath, markerDir := findMarkerForPathWithin(path, ".htaccess", scanRoot)
	wantMarker := filepath.Join(scanRoot, ".htaccess")
	if markerPath != wantMarker {
		t.Errorf("expected marker path %q, got %q", wantMarker, markerPath)
	}
	if markerDir != scanRoot {
		t.Errorf("expected marker dir %q, got %q", scanRoot, markerDir)
	}
}

func TestResolveRestoreJobIgnoresMarkerAboveScanRoot(t *testing.T) {
	dir := t.TempDir()
	// Marker is in the parent of the scan root.
	createMarker(t, dir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^content/video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)

	scanRoot := filepath.Join(dir, "content")
	if err := os.MkdirAll(scanRoot, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(scanRoot, "video.mp4")
	createOldFile(t, path)

	cfg := &config.Config{
		ScanRoots:       []string{scanRoot},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	d := NewDaemon(cfg, false, nil, nil)
	job, ok := d.resolveRestoreJob(path)
	if ok {
		t.Fatalf("expected resolve to fail because marker is above scan_root, got job=%+v", job)
	}
}

func TestResolveRestoreJobFindsMarkerInsideScanRoot(t *testing.T) {
	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "content")
	createMarker(t, scanRoot, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)

	path := filepath.Join(scanRoot, "video.mp4")
	createOldFile(t, path)

	cfg := &config.Config{
		ScanRoots:       []string{scanRoot},
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
	if job.RemoteID != "669872d3d3586a56f9a3dfad" {
		t.Errorf("unexpected remote id: %s", job.RemoteID)
	}
	if job.ScanRoot != scanRoot {
		t.Errorf("unexpected scan root: %s", job.ScanRoot)
	}
}

func TestResolveRestoreJobOutsideScanRootsNotRestored(t *testing.T) {
	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "content")
	outsideDir := filepath.Join(dir, "other")
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(outsideDir, "video.mp4")
	createOldFile(t, path)

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
		ScanRoots:       []string{scanRoot},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	d := NewDaemon(cfg, false, database, nil)
	job, ok := d.resolveRestoreJob(path)
	if ok {
		t.Fatalf("expected resolve to fail for path outside scan roots, got job=%+v", job)
	}
}

func TestReconcileDBSkipsPathsOutsideScanRoots(t *testing.T) {
	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "content")
	outsideDir := filepath.Join(dir, "other")
	siblingDir := filepath.Join(dir, "content2")

	for _, d := range []string{scanRoot, outsideDir, siblingDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	insidePath := filepath.Join(scanRoot, "video.mp4")
	outsidePath := filepath.Join(outsideDir, "video.mp4")
	siblingPath := filepath.Join(siblingDir, "video.mp4")

	// Insert records for all three files (none exist on disk).
	for _, p := range []string{insidePath, outsidePath, siblingPath} {
		if err := database.UpsertOffloadedFile(p, "remote-id", 1, 100, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		ScanRoots:       []string{scanRoot},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	d := NewDaemon(cfg, false, database, nil)
	if err := d.reconcileDB(); err != nil {
		t.Fatalf("reconcileDB failed: %v", err)
	}

	// Inside path: file is missing, so reconcileRecord should delete the DB row.
	_, _, _, _, err = database.GetOffloadedFile(insidePath)
	if err == nil {
		t.Error("expected DB record for inside path to be removed because file is missing")
	}

	// Outside path: should be skipped, DB row must remain.
	_, _, _, _, err = database.GetOffloadedFile(outsidePath)
	if err != nil {
		t.Errorf("expected DB record for outside path to be preserved, got error: %v", err)
	}

	// Sibling path: should be skipped, DB row must remain.
	_, _, _, _, err = database.GetOffloadedFile(siblingPath)
	if err != nil {
		t.Errorf("expected DB record for sibling path to be preserved, got error: %v", err)
	}

	// No restore jobs should have been enqueued for outside or sibling paths.
	d.inFlightMu.Lock()
	if d.inFlight[outsidePath] {
		t.Error("expected no restore job for outside path")
	}
	if d.inFlight[siblingPath] {
		t.Error("expected no restore job for sibling path")
	}
	d.inFlightMu.Unlock()
}

func TestReconcileDBPreservesOffloadedMarkerOutsideScanRoots(t *testing.T) {
	dir := t.TempDir()
	scanRoot := filepath.Join(dir, "content")
	siblingDir := filepath.Join(dir, "content2")

	for _, d := range []string{scanRoot, siblingDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer database.Close()

	siblingPath := filepath.Join(siblingDir, "video.mp4")
	createSparseFile(t, siblingPath)

	// Create .offloaded marker.
	if err := os.WriteFile(siblingPath+".offloaded", []byte("offloaded"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := database.UpsertOffloadedFile(siblingPath, "remote-id", 1, 100, time.Now()); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ScanRoots:       []string{scanRoot},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
		RestoreWorkers:  1,
		ScanInterval:    config.Duration(24 * time.Hour),
	}

	d := NewDaemon(cfg, false, database, nil)
	if err := d.reconcileDB(); err != nil {
		t.Fatalf("reconcileDB failed: %v", err)
	}

	// The .offloaded marker must NOT be removed.
	if _, err := os.Stat(siblingPath + ".offloaded"); os.IsNotExist(err) {
		t.Error("expected .offloaded marker to be preserved for path outside scan_roots")
	}

	// DB row must also remain.
	_, _, _, _, err = database.GetOffloadedFile(siblingPath)
	if err != nil {
		t.Errorf("expected DB record to be preserved, got error: %v", err)
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
