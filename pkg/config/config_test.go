package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := `
scan_roots:
  - /tmp/scan1
  - /tmp/scan2
candidate_globs:
  - "**/4k/*.mp4"
marker_filename: ".htaccess"
minimum_age: "720h"
keep_prefix_bytes: 52428800
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(cfg.ScanRoots) != 2 {
		t.Errorf("expected 2 scan roots, got %d", len(cfg.ScanRoots))
	}
	if len(cfg.CandidateGlobs) != 1 {
		t.Errorf("expected 1 glob, got %d", len(cfg.CandidateGlobs))
	}
	if cfg.MarkerFilename != ".htaccess" {
		t.Errorf("expected .htaccess, got %s", cfg.MarkerFilename)
	}
	if cfg.MinimumAge.Duration() != 720*time.Hour {
		t.Errorf("expected 720h, got %v", cfg.MinimumAge)
	}
	if cfg.KeepPrefixBytes != 52428800 {
		t.Errorf("expected 52428800, got %d", cfg.KeepPrefixBytes)
	}
}

func TestLoadConfigWithDays(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := `
scan_roots:
  - /tmp/scan1
candidate_globs:
  - "**/*.mp4"
marker_filename: ".htaccess"
minimum_age: "30d"
keep_prefix_bytes: 1
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if cfg.MinimumAge.Duration() != 30*24*time.Hour {
		t.Errorf("expected 30d, got %v", cfg.MinimumAge)
	}
}

func TestLoadConfigWithMonthsAndYears(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := `
scan_roots:
  - /tmp/scan1
candidate_globs:
  - "**/*.mp4"
marker_filename: ".htaccess"
minimum_age: "1y 6mo"
keep_prefix_bytes: 1
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	expected := 365*24*time.Hour + 180*24*time.Hour
	if cfg.MinimumAge.Duration() != expected {
		t.Errorf("expected 1y6mo (%v), got %v", expected, cfg.MinimumAge)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestValidateMissingScanRoots(t *testing.T) {
	cfg := &Config{
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing scan_roots")
	}
}

func TestValidateMissingCandidateGlobs(t *testing.T) {
	cfg := &Config{
		ScanRoots:       []string{"/tmp"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing candidate_globs")
	}
}

func TestValidateMarkerFilenameDefault(t *testing.T) {
	cfg := &Config{
		ScanRoots:       []string{"/tmp"},
		CandidateGlobs:  []string{"**/*.mp4"},
		MinimumAge:      Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MarkerFilename != ".htaccess" {
		t.Errorf("expected default marker_filename .htaccess, got %s", cfg.MarkerFilename)
	}
}

func TestValidateInvalidMinimumAge(t *testing.T) {
	cfg := &Config{
		ScanRoots:       []string{"/tmp"},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      0,
		KeepPrefixBytes: 1,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid minimum_age")
	}
}

func TestValidateKeepPrefixBytesDefault(t *testing.T) {
	cfg := &Config{
		ScanRoots:       []string{"/tmp"},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      Duration(1 * time.Hour),
		KeepPrefixBytes: 0,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.KeepPrefixBytes != 50*1024*1024 {
		t.Errorf("expected default keep_prefix_bytes 52428800, got %d", cfg.KeepPrefixBytes)
	}
}

func TestValidateMarkerDepthDefault(t *testing.T) {
	cfg := &Config{
		ScanRoots:       []string{"/tmp"},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MarkerDepth != 1 {
		t.Errorf("expected default marker_depth 1, got %d", cfg.MarkerDepth)
	}
}

func TestValidateMarkerDepthExplicit(t *testing.T) {
	cfg := &Config{
		ScanRoots:       []string{"/tmp"},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MarkerDepth:     2,
		MinimumAge:      Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MarkerDepth != 2 {
		t.Errorf("expected marker_depth 2, got %d", cfg.MarkerDepth)
	}
}

func TestValidateMarkerDepthNegative(t *testing.T) {
	cfg := &Config{
		ScanRoots:       []string{"/tmp"},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MarkerDepth:     -1,
		MinimumAge:      Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative marker_depth")
	}
}

func TestLoadConfigWithDatabasePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := `
scan_roots:
  - /tmp/scan1
candidate_globs:
  - "**/*.mp4"
marker_filename: ".htaccess"
minimum_age: "720h"
keep_prefix_bytes: 52428800
database_path: "/var/lib/media-offload/db.db"
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if cfg.DatabasePath != "/var/lib/media-offload/db.db" {
		t.Errorf("unexpected database_path: %s", cfg.DatabasePath)
	}
}

func TestValidateScanIntervalDefault(t *testing.T) {
	cfg := &Config{
		ScanRoots:       []string{"/tmp"},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ScanInterval.Duration() != 24*time.Hour {
		t.Errorf("expected default scan_interval 24h, got %v", cfg.ScanInterval.Duration())
	}
}

func TestValidateRestoreWorkersDefault(t *testing.T) {
	cfg := &Config{
		ScanRoots:       []string{"/tmp"},
		CandidateGlobs:  []string{"**/*.mp4"},
		MarkerFilename:  ".htaccess",
		MinimumAge:      Duration(1 * time.Hour),
		KeepPrefixBytes: 1,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RestoreWorkers != 4 {
		t.Errorf("expected default restore_workers 4, got %d", cfg.RestoreWorkers)
	}
}

func TestLoadConfigWithDaemonFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := `
scan_roots:
  - /tmp/scan1
candidate_globs:
  - "**/*.mp4"
marker_filename: ".htaccess"
minimum_age: "720h"
keep_prefix_bytes: 52428800
scan_interval: "6h"
restore_workers: 8
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if cfg.ScanInterval.Duration() != 6*time.Hour {
		t.Errorf("expected scan_interval 6h, got %v", cfg.ScanInterval.Duration())
	}
	if cfg.RestoreWorkers != 8 {
		t.Errorf("expected restore_workers 8, got %d", cfg.RestoreWorkers)
	}
}
