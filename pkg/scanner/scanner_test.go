package scanner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmilitzer/xvid-media-offload/pkg/config"
	"golang.org/x/sys/unix"
)

func TestScanNoCandidates(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := Scan(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("expected 0 candidates, got %d", len(res.Candidates))
	}
}

func TestScanSkipsNonMP4(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, "#Xvid AutoGraph content protection system.\n")
	createFile(t, filepath.Join(setDir, "video.mkv"), "")

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := Scan(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("expected 0 candidates, got %d", len(res.Candidates))
	}
}

func TestScanSkipsTooYoung(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createFile(t, filepath.Join(setDir, "video.mp4"), "")
	// File is brand new.

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(24 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := Scan(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("expected 0 candidates, got %d", len(res.Candidates))
	}
	if res.SkippedTooYoung != 1 {
		t.Errorf("expected 1 too young, got %d", res.SkippedTooYoung)
	}
}

func TestScanSkipsOffloaded(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createOldFile(t, filepath.Join(setDir, "video.mp4"))
	createFile(t, filepath.Join(setDir, "video.mp4.offloaded"), "")

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := Scan(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("expected 0 candidates, got %d", len(res.Candidates))
	}
	if res.SkippedOffloaded != 1 {
		t.Errorf("expected 1 offloaded skip, got %d", res.SkippedOffloaded)
	}
}

func TestScanSkipsMissingRemoteID(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	// video.mp4 is listed in the marker but does not exist on disk.

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := Scan(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("expected 0 candidates, got %d", len(res.Candidates))
	}
	if res.SkippedMissingRemote != 1 {
		t.Errorf("expected 1 missing remote id skip, got %d", res.SkippedMissingRemote)
	}
}

func TestScanIgnoresInvalidMarker(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, "#Invalid marker\n")
	createOldFile(t, filepath.Join(setDir, "video.mp4"))

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := Scan(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("expected 0 candidates, got %d", len(res.Candidates))
	}
	if res.InvalidMarkers != 1 {
		t.Errorf("expected 1 invalid marker, got %d", res.InvalidMarkers)
	}
}

func TestScanFindsCandidate(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createOldFile(t, filepath.Join(setDir, "video.mp4"))

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := Scan(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(res.Candidates))
	}
	c := res.Candidates[0]
	if c.RemoteID != "669872d3d3586a56f9a3dfad" {
		t.Errorf("unexpected remote id: %s", c.RemoteID)
	}
	if res.ValidMarkers != 1 {
		t.Errorf("expected 1 valid marker, got %d", res.ValidMarkers)
	}
}

func TestScanFindsNestedCandidate(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^720p/video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createOldFile(t, filepath.Join(setDir, "720p", "video.mp4"))

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := Scan(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(res.Candidates))
	}
	c := res.Candidates[0]
	if c.RemoteID != "669872d3d3586a56f9a3dfad" {
		t.Errorf("unexpected remote id: %s", c.RemoteID)
	}
}

func TestScanGlobMatching(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
RewriteRule "^720p/video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfbf
`)
	createOldFile(t, filepath.Join(setDir, "4k", "video.mp4"))
	createOldFile(t, filepath.Join(setDir, "720p", "video.mp4"))

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/4k/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := Scan(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(res.Candidates))
	}
	if res.Candidates[0].Glob != "**/4k/*.mp4" {
		t.Errorf("unexpected glob: %s", res.Candidates[0].Glob)
	}
}

func TestScanDepthTwo(t *testing.T) {
	dir := t.TempDir()
	// Depth-2 layout: scan_root/group1/set1/.htaccess
	setDir := filepath.Join(dir, "group1", "set1")
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
		MarkerDepth:     2,
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := Scan(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(res.Candidates))
	}
	c := res.Candidates[0]
	if c.RemoteID != "669872d3d3586a56f9a3dfad" {
		t.Errorf("unexpected remote id: %s", c.RemoteID)
	}
	if res.ValidMarkers != 1 {
		t.Errorf("expected 1 valid marker, got %d", res.ValidMarkers)
	}
}

func TestScanVerboseRecordsSkipsAndErrors(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createFile(t, filepath.Join(setDir, "video.mp4"), "")
	// File is brand new -> too young.

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(24 * time.Hour),
		KeepPrefixBytes: 1,
		Verbose:         true,
	}

	res, err := Scan(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("expected 0 candidates, got %d", len(res.Candidates))
	}
	if res.SkippedTooYoung != 1 {
		t.Errorf("expected 1 too young, got %d", res.SkippedTooYoung)
	}
	if len(res.SkippedDetails) != 1 {
		t.Fatalf("expected 1 skip detail, got %d", len(res.SkippedDetails))
	}
	if res.SkippedDetails[0].Reason != "too young" {
		t.Errorf("unexpected reason: %s", res.SkippedDetails[0].Reason)
	}
}

func TestScanAllReturnsAllFiles(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createOldFile(t, filepath.Join(setDir, "video.mp4"))

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := ScanAll(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(res.Files))
	}
	if res.Files[0].Path != filepath.Join(setDir, "video.mp4") {
		t.Errorf("unexpected path: %s", res.Files[0].Path)
	}
	if res.Files[0].HasOffloadedMarker {
		t.Error("expected no offloaded marker")
	}
}

func TestScanForRestoreIgnoresNonSparse(t *testing.T) {
	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createOldFile(t, filepath.Join(setDir, "video.mp4"))

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := ScanForRestore(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Files) != 0 {
		t.Errorf("expected 0 restore candidates for non-sparse file, got %d", len(res.Files))
	}
}

func TestScanForRestoreIgnoresFilesWithMarker(t *testing.T) {
	if os.Getenv("RUN_HOLE_PUNCH_TESTS") != "1" {
		t.Skip("Set RUN_HOLE_PUNCH_TESTS=1 to run hole-punch dependent tests")
	}

	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	path := filepath.Join(setDir, "video.mp4")
	createLargeOldFile(t, path)

	// Punch a hole to make it sparse.
	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, 1024, 1024*1024)
	f.Close()

	// Create .offloaded marker.
	createFile(t, path+".offloaded", "")

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := ScanForRestore(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Files) != 0 {
		t.Errorf("expected 0 restore candidates when .offloaded marker exists, got %d", len(res.Files))
	}
}

func TestScanForRestoreFindsSparseWithoutMarker(t *testing.T) {
	if os.Getenv("RUN_HOLE_PUNCH_TESTS") != "1" {
		t.Skip("Set RUN_HOLE_PUNCH_TESTS=1 to run hole-punch dependent tests")
	}

	dir := t.TempDir()
	setDir := filepath.Join(dir, "set1")
	createMarker(t, setDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	path := filepath.Join(setDir, "video.mp4")
	createLargeOldFile(t, path)

	// Punch a hole to make it sparse.
	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, 1024, 1024*1024)
	f.Close()

	// Ensure .offloaded marker is absent.
	os.Remove(path + ".offloaded")

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}

	res, err := ScanForRestore(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("expected 1 restore candidate, got %d", len(res.Files))
	}
	if res.Files[0].Path != path {
		t.Errorf("unexpected path: %s", res.Files[0].Path)
	}
}

func TestScanIntegration(t *testing.T) {
	// Build a self-contained directory tree so we control mtimes explicitly.
	dir := t.TempDir()

	// Valid managed folder with a 4k candidate, a too-young file,
	// an offloaded file, a missing file, and a non-MP4 file.
	validDir := filepath.Join(dir, "valid")
	createMarker(t, validDir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/testvideo.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfc0
RewriteEngine on
RewriteRule "^4k/tooyoung.mp4" - [F,L,NC]
#file_id=111111111111111111111111
RewriteEngine on
RewriteRule "^4k/offloaded.mp4" - [F,L,NC]
#file_id=222222222222222222222222
RewriteEngine on
RewriteRule "^4k/missing.mp4" - [F,L,NC]
#file_id=333333333333333333333333
`)
	oldTime := time.Now().Add(-720 * time.Hour)
	youngTime := time.Now().Add(-1 * time.Hour)

	createFile(t, filepath.Join(validDir, "4k", "testvideo.mp4"), "")
	os.Chtimes(filepath.Join(validDir, "4k", "testvideo.mp4"), oldTime, oldTime)

	createFile(t, filepath.Join(validDir, "4k", "tooyoung.mp4"), "")
	os.Chtimes(filepath.Join(validDir, "4k", "tooyoung.mp4"), youngTime, youngTime)

	createFile(t, filepath.Join(validDir, "4k", "offloaded.mp4"), "")
	os.Chtimes(filepath.Join(validDir, "4k", "offloaded.mp4"), oldTime, oldTime)
	createFile(t, filepath.Join(validDir, "4k", "offloaded.mp4.offloaded"), "")

	// missing.mp4 is listed in marker but not created on disk.

	createFile(t, filepath.Join(validDir, "4k", "ignore.mkv"), "")
	os.Chtimes(filepath.Join(validDir, "4k", "ignore.mkv"), oldTime, oldTime)

	// Invalid marker folder.
	invalidDir := filepath.Join(dir, "invalid-marker")
	createMarker(t, invalidDir, "#Invalid marker\n")
	createFile(t, filepath.Join(invalidDir, "4k", "testvideo.mp4"), "")
	os.Chtimes(filepath.Join(invalidDir, "4k", "testvideo.mp4"), oldTime, oldTime)

	// No-marker folder.
	noMarkerDir := filepath.Join(dir, "no-marker")
	createFile(t, filepath.Join(noMarkerDir, "4k", "testvideo.mp4"), "")
	os.Chtimes(filepath.Join(noMarkerDir, "4k", "testvideo.mp4"), oldTime, oldTime)

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/4k/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      config.Duration(720 * time.Hour),
		KeepPrefixBytes: 52428800,
	}

	res, err := Scan(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// We expect exactly 1 candidate: valid/4k/testvideo.mp4
	if len(res.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(res.Candidates))
	}
	c := res.Candidates[0]
	if c.RemoteID != "669872d3d3586a56f9a3dfc0" {
		t.Errorf("unexpected remote id: %s", c.RemoteID)
	}

	if res.ValidMarkers != 1 {
		t.Errorf("expected 1 valid marker, got %d", res.ValidMarkers)
	}
	if res.InvalidMarkers != 1 {
		t.Errorf("expected 1 invalid marker, got %d", res.InvalidMarkers)
	}
	if res.SkippedOffloaded != 1 {
		t.Errorf("expected 1 offloaded skip, got %d", res.SkippedOffloaded)
	}
	if res.SkippedTooYoung != 1 {
		t.Errorf("expected 1 too young skip, got %d", res.SkippedTooYoung)
	}
	if res.SkippedMissingRemote != 1 {
		t.Errorf("expected 1 missing remote id skip, got %d", res.SkippedMissingRemote)
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

func createLargeOldFile(t *testing.T, path string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 2*1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	f.Close()
	oldTime := time.Now().Add(-720 * time.Hour)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
}
