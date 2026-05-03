package scanner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/mmilitzer/xvid-media-offload/pkg/config"
	"github.com/mmilitzer/xvid-media-offload/pkg/marker"
)

// Candidate represents a file that is eligible for offloading.
type Candidate struct {
	Path       string
	RelPath    string
	Size       int64
	ModTime    time.Time
	Age        time.Duration
	RemoteID   string
	Glob       string
	MarkerPath string
}

// Result holds the outcome of a scan.
type Result struct {
	Candidates           []Candidate
	ManagedFolders       int
	ValidMarkers         int
	InvalidMarkers       int
	SkippedOffloaded     int
	SkippedTooYoung      int
	SkippedMissingRemote int
	Errors               int
	TotalCandidateSize   int64
}

// markerCacheEntry tracks whether a directory (or ancestor) has a valid marker.
type markerCacheEntry struct {
	info       *marker.Info
	markerPath string
	found      bool
}

// Scan walks the configured scan roots and discovers offload candidates.
func Scan(cfg *config.Config) (*Result, error) {
	res := &Result{
		Candidates: make([]Candidate, 0),
	}

	for _, root := range cfg.ScanRoots {
		if err := scanRoot(root, cfg, res); err != nil {
			return nil, fmt.Errorf("scanning root %s: %w", root, err)
		}
	}

	return res, nil
}

func scanRoot(root string, cfg *config.Config, res *Result) error {
	root = filepath.Clean(root)

	// Cache marker lookups per directory to avoid re-parsing.
	markerCache := make(map[string]*markerCacheEntry)

	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			res.Errors++
			return nil // continue walking
		}

		if d.IsDir() {
			return nil
		}

		relToRoot, err := filepath.Rel(root, path)
		if err != nil {
			res.Errors++
			return nil
		}

		// Safety: only consider .mp4 files regardless of glob.
		if !strings.HasSuffix(strings.ToLower(path), ".mp4") {
			return nil
		}

		// Check candidate globs.
		matchedGlob := ""
		for _, g := range cfg.CandidateGlobs {
			// Match against path relative to scan root, using forward slashes.
			matchPath := filepath.ToSlash(relToRoot)
			if ok, _ := doublestar.Match(g, matchPath); ok {
				matchedGlob = g
				break
			}
		}
		if matchedGlob == "" {
			return nil
		}

		// Look for a valid marker in this file's directory or ancestors.
		dir := filepath.Dir(path)
		mce := findMarker(dir, root, cfg.MarkerFilename, markerCache, res)
		if mce == nil || !mce.found {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			res.Errors++
			return nil
		}

		// Check for .offloaded sibling.
		offloadedPath := path + ".offloaded"
		if _, err := os.Stat(offloadedPath); err == nil {
			res.SkippedOffloaded++
			return nil
		}

		// Check minimum age.
		age := time.Since(info.ModTime())
		if age < cfg.MinimumAge {
			res.SkippedTooYoung++
			return nil
		}

		// Determine relative path from the marker's base directory.
		markerDir := filepath.Dir(mce.markerPath)
		relToMarker, err := filepath.Rel(markerDir, path)
		if err != nil {
			res.Errors++
			return nil
		}
		relToMarker = filepath.ToSlash(relToMarker)

		remoteID, ok := mce.info.MatchFileID(relToMarker)
		if !ok {
			res.SkippedMissingRemote++
			return nil
		}

		res.Candidates = append(res.Candidates, Candidate{
			Path:       path,
			RelPath:    relToRoot,
			Size:       info.Size(),
			ModTime:    info.ModTime(),
			Age:        age,
			RemoteID:   remoteID,
			Glob:       matchedGlob,
			MarkerPath: mce.markerPath,
		})
		res.TotalCandidateSize += info.Size()

		return nil
	})
}

// findMarker searches for a marker file starting at dir and walking up to root.
// Results are cached in markerCache.
func findMarker(dir, root, markerFilename string, cache map[string]*markerCacheEntry, res *Result) *markerCacheEntry {
	if entry, ok := cache[dir]; ok {
		return entry
	}

	markerPath := filepath.Join(dir, markerFilename)
	if _, err := os.Stat(markerPath); err == nil {
		info, parseErr := marker.Parse(markerPath)
		if parseErr != nil {
			res.Errors++
			cache[dir] = &markerCacheEntry{found: false}
			return cache[dir]
		}
		res.ManagedFolders++
		if info.Valid {
			res.ValidMarkers++
			cache[dir] = &markerCacheEntry{
				info:       info,
				markerPath: markerPath,
				found:      true,
			}
		} else {
			res.InvalidMarkers++
			cache[dir] = &markerCacheEntry{found: false}
		}
		return cache[dir]
	}

	// No marker here; check parent if we haven't reached root.
	parent := filepath.Dir(dir)
	if parent == dir || !strings.HasPrefix(dir, root) {
		cache[dir] = &markerCacheEntry{found: false}
		return cache[dir]
	}

	parentEntry := findMarker(parent, root, markerFilename, cache, res)
	cache[dir] = parentEntry
	return parentEntry
}
