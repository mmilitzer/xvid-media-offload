package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Ownership policy constants.
const (
	OwnershipPolicyRequireDaemonOwner           = "require-daemon-owner"
	OwnershipPolicyAllowOwnerMismatch           = "allow-owner-mismatch"
	OwnershipPolicyReplaceWithDaemonOwnedSparse = "replace-with-daemon-owned-sparse-copy"
)

// Config holds the application configuration.
type Config struct {
	ScanRoots       []string `yaml:"scan_roots"`
	CandidateGlobs  []string `yaml:"candidate_globs"`
	MarkerFilename  string   `yaml:"marker_filename"`
	MarkerDepth     int      `yaml:"marker_depth"`
	MinimumAge      Duration `yaml:"minimum_age"`
	KeepPrefixBytes int64    `yaml:"keep_prefix_bytes"`
	DatabasePath    string   `yaml:"database_path"`
	ScanInterval    Duration `yaml:"scan_interval"`
	RestoreWorkers  int      `yaml:"restore_workers"`
	// LockFile is the path to the daemon's advisory lock file.
	// It must be writable by the user running the daemon.
	// When empty, the daemon command defaults to a file next to the config.
	LockFile string `yaml:"lock_file"`
	// DownloadTimeout is the global HTTP timeout for file downloads.
	// When empty or zero, defaults to 6 hours.
	DownloadTimeout Duration `yaml:"download_timeout"`
	// MarkerFileMode controls the permissions of created .offloaded marker files.
	// When empty or "same-as-source" (the default), markers inherit the
	// permission bits of the source media file.  Any other value is parsed as
	// an octal string (e.g. "0666") and applied literally.
	MarkerFileMode string `yaml:"marker_file_mode"`
	// OwnershipPolicy controls offload/shrink eligibility and the offload
	// strategy for MP4 files that are not owned by the daemon user.
	// Restore is independent of this setting and always uses best-effort
	// metadata preservation.
	//   require-daemon-owner           - only offload files owned by the daemon user (default)
	//   allow-owner-mismatch           - allow offloading non-owned files with best-effort preservation
	//   replace-with-daemon-owned-sparse-copy - replace non-owned files with a daemon-owned sparse copy
	OwnershipPolicy string `yaml:"ownership_policy"`
	Verbose         bool   `yaml:"-"` // set from CLI flag
}

// Load reads and parses a YAML configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

const defaultMarkerFilename = ".htaccess"
const defaultKeepPrefixBytes = 50 * 1024 * 1024 // 50 MB
const defaultScanInterval = 24 * time.Hour
const defaultRestoreWorkers = 4
const defaultDownloadTimeout = 6 * time.Hour

// Validate checks that all required fields are present and valid.
// Optional fields are set to their defaults when empty or zero.
func (c *Config) Validate() error {
	if len(c.ScanRoots) == 0 {
		return fmt.Errorf("config validation failed: scan_roots is required")
	}
	if len(c.CandidateGlobs) == 0 {
		return fmt.Errorf("config validation failed: candidate_globs is required")
	}
	if c.MarkerFilename == "" {
		c.MarkerFilename = defaultMarkerFilename
	}
	if c.MarkerDepth < 0 {
		return fmt.Errorf("config validation failed: marker_depth must be a non-negative integer")
	}
	if c.MarkerDepth == 0 {
		c.MarkerDepth = 1
	}
	if c.MinimumAge.Duration() <= 0 {
		return fmt.Errorf("config validation failed: minimum_age must be a positive duration")
	}
	if c.KeepPrefixBytes <= 0 {
		c.KeepPrefixBytes = defaultKeepPrefixBytes
	}
	if c.ScanInterval.Duration() <= 0 {
		c.ScanInterval = Duration(defaultScanInterval)
	}
	if c.RestoreWorkers <= 0 {
		c.RestoreWorkers = defaultRestoreWorkers
	}
	if c.DownloadTimeout.Duration() <= 0 {
		c.DownloadTimeout = Duration(defaultDownloadTimeout)
	}
	if err := c.validateMarkerFileMode(); err != nil {
		return err
	}
	if err := c.validateOwnershipPolicy(); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateOwnershipPolicy() error {
	if c.OwnershipPolicy == "" {
		c.OwnershipPolicy = OwnershipPolicyRequireDaemonOwner
		return nil
	}
	switch c.OwnershipPolicy {
	case OwnershipPolicyRequireDaemonOwner, OwnershipPolicyAllowOwnerMismatch, OwnershipPolicyReplaceWithDaemonOwnedSparse:
		return nil
	default:
		return fmt.Errorf("config validation failed: ownership_policy must be one of %q, %q, or %q",
			OwnershipPolicyRequireDaemonOwner, OwnershipPolicyAllowOwnerMismatch, OwnershipPolicyReplaceWithDaemonOwnedSparse)
	}
}

func (c *Config) validateMarkerFileMode() error {
	if c.MarkerFileMode == "" || c.MarkerFileMode == "same-as-source" {
		return nil
	}
	u, err := strconv.ParseUint(c.MarkerFileMode, 8, 32)
	if err != nil {
		return fmt.Errorf("config validation failed: marker_file_mode must be \"same-as-source\" or a valid octal mode (e.g. \"0666\"): %w", err)
	}
	if u > 0777 {
		return fmt.Errorf("config validation failed: marker_file_mode %q exceeds maximum permission bits 0777", c.MarkerFileMode)
	}
	return nil
}

// ResolveMarkerFileMode returns the file mode to use for an .offloaded marker
// based on the source media file and the MarkerFileMode setting.
func (c *Config) ResolveMarkerFileMode(sourcePath string) (os.FileMode, error) {
	if c.MarkerFileMode == "" || c.MarkerFileMode == "same-as-source" {
		fi, err := os.Stat(sourcePath)
		if err != nil {
			return 0, err
		}
		return fi.Mode() & os.ModePerm, nil
	}
	u, err := strconv.ParseUint(c.MarkerFileMode, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid marker_file_mode %q: %w", c.MarkerFileMode, err)
	}
	return os.FileMode(u), nil
}

// CreateOffloadedMarker writes an .offloaded marker file with permissions
// determined by the configuration.  The mode is applied explicitly with
// Chmod so that umask does not interfere.
func (c *Config) CreateOffloadedMarker(sourcePath, markerPath string, content []byte) error {
	mode, err := c.ResolveMarkerFileMode(sourcePath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(markerPath, content, mode); err != nil {
		return err
	}
	return os.Chmod(markerPath, mode)
}
