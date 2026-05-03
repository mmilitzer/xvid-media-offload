package scanner

import (
	"fmt"
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
	Autograph  int // -1 means not set, 0 or 1 means explicit value
	Glob       string
	MarkerPath string
}

// SkipInfo records a file that was skipped during scanning.
type SkipInfo struct {
	Path       string
	MarkerPath string
	Reason     string
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
	SkippedDetails       []SkipInfo
	ErrorDetails         []string
}

// Scan discovers offload candidates by looking for marker files at the
// configured depth below each scan root, deriving candidate file paths
// directly from the marker's RewriteRule patterns, and checking those
// specific files.
func Scan(cfg *config.Config) (*Result, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

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

	dirs, err := dirsAtDepth(root, cfg.MarkerDepth)
	if err != nil {
		return fmt.Errorf("listing directories at depth %d under %s: %w", cfg.MarkerDepth, root, err)
	}

	for _, dirPath := range dirs {
		markerPath := filepath.Join(dirPath, cfg.MarkerFilename)

		if _, err := os.Stat(markerPath); os.IsNotExist(err) {
			continue // no marker here, not an error
		}

		mInfo, err := marker.Parse(markerPath)
		if err != nil {
			res.Errors++
			msg := fmt.Sprintf("marker %s: %v", markerPath, err)
			if cfg.Verbose {
				res.ErrorDetails = append(res.ErrorDetails, msg)
				fmt.Fprintln(os.Stderr, msg)
			}
			continue
		}

		res.ManagedFolders++
		if !mInfo.Valid {
			res.InvalidMarkers++
			if cfg.Verbose {
				msg := fmt.Sprintf("marker %s: invalid marker file", markerPath)
				res.ErrorDetails = append(res.ErrorDetails, msg)
				fmt.Fprintln(os.Stderr, msg)
			}
			continue
		}
		res.ValidMarkers++

		for _, pattern := range mInfo.Patterns {
			relPath := literalPathFromRegex(pattern)

			// Safety: only consider .mp4 files regardless of glob.
			if !strings.HasSuffix(strings.ToLower(relPath), ".mp4") {
				continue
			}

			// Check candidate globs against the path relative to the scan root.
			relToRoot, err := filepath.Rel(root, filepath.Join(dirPath, filepath.FromSlash(relPath)))
			if err != nil {
				res.Errors++
				msg := fmt.Sprintf("relative path error for %s: %v", filepath.Join(dirPath, relPath), err)
				if cfg.Verbose {
					res.ErrorDetails = append(res.ErrorDetails, msg)
					fmt.Fprintln(os.Stderr, msg)
				}
				continue
			}
			relToRoot = filepath.ToSlash(relToRoot)
			matchedGlob := ""
			for _, g := range cfg.CandidateGlobs {
				if matched, _ := doublestar.Match(g, relToRoot); matched {
					matchedGlob = g
					break
				}
			}
			if matchedGlob == "" {
				continue
			}

			absPath := filepath.Join(dirPath, filepath.FromSlash(relPath))

			fileInfo, err := os.Stat(absPath)
			if err != nil {
				// File listed in marker but not present on disk.
				res.SkippedMissingRemote++
				if cfg.Verbose {
					res.SkippedDetails = append(res.SkippedDetails, SkipInfo{
						Path:       absPath,
						MarkerPath: markerPath,
						Reason:     "file not found on disk",
					})
				}
				continue
			}

			// Check for .offloaded sibling.
			if _, err := os.Stat(absPath + ".offloaded"); err == nil {
				res.SkippedOffloaded++
				if cfg.Verbose {
					res.SkippedDetails = append(res.SkippedDetails, SkipInfo{
						Path:       absPath,
						MarkerPath: markerPath,
						Reason:     "already offloaded",
					})
				}
				continue
			}

			// Check minimum age.
			age := time.Since(fileInfo.ModTime())
			if age < cfg.MinimumAge.Duration() {
				res.SkippedTooYoung++
				if cfg.Verbose {
					res.SkippedDetails = append(res.SkippedDetails, SkipInfo{
						Path:       absPath,
						MarkerPath: markerPath,
						Reason:     "too young",
					})
				}
				continue
			}

			remoteID := mInfo.FileIDByRel[pattern]
			autograph := -1
			if a, ok := mInfo.MatchAutograph(relPath); ok {
				autograph = a
			}

			res.Candidates = append(res.Candidates, Candidate{
				Path:       absPath,
				RelPath:    relToRoot,
				Size:       fileInfo.Size(),
				ModTime:    fileInfo.ModTime(),
				Age:        age,
				RemoteID:   remoteID,
				Autograph:  autograph,
				Glob:       matchedGlob,
				MarkerPath: markerPath,
			})
			res.TotalCandidateSize += fileInfo.Size()
		}
	}

	return nil
}

// dirsAtDepth returns all directories exactly 'depth' levels below root.
func dirsAtDepth(root string, depth int) ([]string, error) {
	if depth <= 0 {
		return nil, fmt.Errorf("marker_depth must be positive")
	}
	if depth == 1 {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		var dirs []string
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(root, e.Name()))
			}
		}
		return dirs, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subDirs, err := dirsAtDepth(filepath.Join(root, e.Name()), depth-1)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, subDirs...)
	}
	return dirs, nil
}

// literalPathFromRegex extracts the literal relative path from an exact-match
// Apache RewriteRule pattern by stripping the leading ^ and trailing $ anchors.
// The marker regexps for MP4 files are always exact-match patterns.
func literalPathFromRegex(pattern string) string {
	s := pattern
	if strings.HasPrefix(s, "^") {
		s = s[1:]
	}
	if strings.HasSuffix(s, "$") {
		s = s[:len(s)-1]
	}
	return s
}
