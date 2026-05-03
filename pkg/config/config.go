package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the application configuration.
type Config struct {
	ScanRoots       []string      `yaml:"scan_roots"`
	CandidateGlobs  []string      `yaml:"candidate_globs"`
	MarkerFilename  string        `yaml:"marker_filename"`
	MarkerDepth     int           `yaml:"marker_depth"`
	MinimumAge      time.Duration `yaml:"minimum_age"`
	KeepPrefixBytes int64         `yaml:"keep_prefix_bytes"`
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

// Validate checks that all required fields are present and valid.
func (c *Config) Validate() error {
	if len(c.ScanRoots) == 0 {
		return fmt.Errorf("config validation failed: scan_roots is required")
	}
	if len(c.CandidateGlobs) == 0 {
		return fmt.Errorf("config validation failed: candidate_globs is required")
	}
	if c.MarkerFilename == "" {
		return fmt.Errorf("config validation failed: marker_filename is required")
	}
	if c.MarkerDepth < 0 {
		return fmt.Errorf("config validation failed: marker_depth must be a non-negative integer")
	}
	if c.MarkerDepth == 0 {
		c.MarkerDepth = 1
	}
	if c.MinimumAge <= 0 {
		return fmt.Errorf("config validation failed: minimum_age must be a positive duration")
	}
	if c.KeepPrefixBytes <= 0 {
		return fmt.Errorf("config validation failed: keep_prefix_bytes must be positive")
	}
	return nil
}
