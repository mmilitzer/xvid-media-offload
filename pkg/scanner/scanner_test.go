package scanner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmilitzer/xvid-media-offload/pkg/config"
)

func TestScanNoCandidates(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      1 * time.Hour,
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
	createMarker(t, dir, "#Xvid AutoGraph content protection system.\n")
	createFile(t, filepath.Join(dir, "video.mkv"), "")

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      1 * time.Hour,
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
	createMarker(t, dir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createFile(t, filepath.Join(dir, "video.mp4"), "")
	// File is brand new.

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      24 * time.Hour,
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
	createMarker(t, dir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createOldFile(t, filepath.Join(dir, "video.mp4"))
	createFile(t, filepath.Join(dir, "video.mp4.offloaded"), "")

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      1 * time.Hour,
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
	createMarker(t, dir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^other.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createOldFile(t, filepath.Join(dir, "video.mp4"))

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      1 * time.Hour,
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
	createMarker(t, dir, "#Invalid marker\n")
	createOldFile(t, filepath.Join(dir, "video.mp4"))

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      1 * time.Hour,
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
	createMarker(t, dir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	createOldFile(t, filepath.Join(dir, "video.mp4"))

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      1 * time.Hour,
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

func TestScanInheritsParentMarker(t *testing.T) {
	dir := t.TempDir()
	createMarker(t, dir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^subdir/video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`)
	sub := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	createOldFile(t, filepath.Join(sub, "video.mp4"))

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      1 * time.Hour,
		KeepPrefixBytes: 1,
	}

	res, err := Scan(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(res.Candidates))
	}
}

func TestScanGlobMatching(t *testing.T) {
	dir := t.TempDir()
	createMarker(t, dir, `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^4k/video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
RewriteRule "^720p/video.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfbf
`)
	createOldFile(t, filepath.Join(dir, "4k", "video.mp4"))
	createOldFile(t, filepath.Join(dir, "720p", "video.mp4"))

	cfg := &config.Config{
		ScanRoots:       []string{dir},
		CandidateGlobs:  []string{"**/4k/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      1 * time.Hour,
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

func TestScanIntegrationWithTestdata(t *testing.T) {
	// This test uses the real testdata directory at the repo root.
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ScanRoots:       []string{filepath.Join(repoRoot, "testdata", "content")},
		CandidateGlobs:  []string{"**/4k/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      720 * time.Hour,
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

	// Counts from testdata:
	// valid/.autograph -> valid
	// invalid-marker/.autograph -> invalid
	// no-marker -> none
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
