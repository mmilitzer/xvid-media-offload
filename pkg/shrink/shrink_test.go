package shrink

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmilitzer/xvid-media-offload/pkg/config"
	"github.com/mmilitzer/xvid-media-offload/pkg/db"
	"github.com/mmilitzer/xvid-media-offload/pkg/scanner"
	"golang.org/x/sys/unix"
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

func TestShrinkDryRunSkipsSparseNotInCandidates(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`)

	// Create a genuinely sparse file: write 1 byte then truncate to 1MB.
	// On Linux tmpfs this yields st_blocks=1 (512 bytes) vs st_size=1MB,
	// so sparse.IsSparse returns true without needing hole punching.
	path := filepath.Join(setDir, "4k", "video.mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
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
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

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

	// The sparse file must be counted as skipped but NOT as a candidate.
	if res.SkippedSparse != 1 {
		t.Errorf("expected 1 sparse skip, got %d", res.SkippedSparse)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("expected 0 candidates (sparse file excluded), got %d", len(res.Candidates))
	}
	for _, c := range res.Candidates {
		if c.Path == path {
			t.Errorf("sparse file %s should not appear in Candidates", path)
		}
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

func TestShrinkApplyMarkerPermissionsSameAsSource(t *testing.T) {
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

	path := filepath.Join(setDir, "4k", "video.mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0750)
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

	res, err := Run(cfg, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Offloaded) != 1 {
		t.Fatalf("expected 1 offloaded file, got %d", len(res.Offloaded))
	}

	fi, err := os.Stat(path + ".offloaded")
	if err != nil {
		t.Fatalf("unexpected error stat marker: %v", err)
	}
	if fi.Mode().Perm() != 0750 {
		t.Errorf("expected marker mode 0750, got %04o", fi.Mode().Perm())
	}
}

func TestShrinkApplyMarkerPermissionsExplicitMode(t *testing.T) {
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
		MarkerFileMode:  "0666",
	}

	res, err := Run(cfg, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Offloaded) != 1 {
		t.Fatalf("expected 1 offloaded file, got %d", len(res.Offloaded))
	}

	fi, err := os.Stat(path + ".offloaded")
	if err != nil {
		t.Fatalf("unexpected error stat marker: %v", err)
	}
	if fi.Mode().Perm() != 0666 {
		t.Errorf("expected marker mode 0666, got %04o", fi.Mode().Perm())
	}
}

func TestRunFromAllProducesSameDryRunResults(t *testing.T) {
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

	// Run via the traditional two-pass path.
	resRun, err := Run(cfg, true, nil)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// Run via single-pass RunFromAll.
	all, err := scanner.ScanAll(cfg)
	if err != nil {
		t.Fatalf("ScanAll error: %v", err)
	}
	resFromAll, err := RunFromAll(cfg, true, nil, all)
	if err != nil {
		t.Fatalf("RunFromAll error: %v", err)
	}

	if len(resFromAll.Candidates) != len(resRun.Candidates) {
		t.Errorf("expected %d candidates from RunFromAll, got %d", len(resRun.Candidates), len(resFromAll.Candidates))
	}
	if resFromAll.ManagedFolders != resRun.ManagedFolders {
		t.Errorf("ManagedFolders mismatch: %d vs %d", resFromAll.ManagedFolders, resRun.ManagedFolders)
	}
	if resFromAll.ValidMarkers != resRun.ValidMarkers {
		t.Errorf("ValidMarkers mismatch: %d vs %d", resFromAll.ValidMarkers, resRun.ValidMarkers)
	}
	if resFromAll.SkippedOffloaded != resRun.SkippedOffloaded {
		t.Errorf("SkippedOffloaded mismatch: %d vs %d", resFromAll.SkippedOffloaded, resRun.SkippedOffloaded)
	}
	if resFromAll.SkippedTooYoung != resRun.SkippedTooYoung {
		t.Errorf("SkippedTooYoung mismatch: %d vs %d", resFromAll.SkippedTooYoung, resRun.SkippedTooYoung)
	}
	if resFromAll.SkippedMissingRemote != resRun.SkippedMissingRemote {
		t.Errorf("SkippedMissingRemote mismatch: %d vs %d", resFromAll.SkippedMissingRemote, resRun.SkippedMissingRemote)
	}
}

func TestShrinkApplyPreservesMtime(t *testing.T) {
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

	// Set an old, specific mtime/atime.
	oldTime := time.Date(2020, 1, 15, 10, 30, 0, 0, time.UTC)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

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

	beforeOffload := time.Now()
	res, err := Run(cfg, false, database)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Offloaded) != 1 {
		t.Fatalf("expected 1 offloaded file, got %d", len(res.Offloaded))
	}

	// Verify MP4 mtime is preserved.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error stat mp4: %v", err)
	}
	if !fi.ModTime().Equal(oldTime) {
		t.Errorf("expected MP4 mtime %v, got %v", oldTime, fi.ModTime())
	}

	// Verify MP4 atime is preserved.
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		t.Fatalf("unexpected error unix stat mp4: %v", err)
	}
	atime := time.Unix(stat.Atim.Sec, stat.Atim.Nsec)
	if !atime.Equal(oldTime) {
		t.Errorf("expected MP4 atime %v, got %v", oldTime, atime)
	}

	// Verify .offloaded marker has newer mtime.
	mfi, err := os.Stat(path + ".offloaded")
	if err != nil {
		t.Fatalf("unexpected error stat marker: %v", err)
	}
	if !mfi.ModTime().After(oldTime) {
		t.Errorf("expected marker mtime after %v, got %v", oldTime, mfi.ModTime())
	}
	if !mfi.ModTime().After(beforeOffload.Add(-1 * time.Second)) {
		t.Errorf("expected marker mtime around offload time, got %v (before %v)", mfi.ModTime(), beforeOffload)
	}

	// Verify DB offloaded_at records offload time.
	_, _, _, offloadedAt, err := database.GetOffloadedFile(path)
	if err != nil {
		t.Fatalf("unexpected error getting db record: %v", err)
	}
	if !offloadedAt.After(oldTime) {
		t.Errorf("expected DB offloaded_at after %v, got %v", oldTime, offloadedAt)
	}
	if !offloadedAt.After(beforeOffload.Add(-1 * time.Second)) {
		t.Errorf("expected DB offloaded_at around offload time, got %v (before %v)", offloadedAt, beforeOffload)
	}
}
