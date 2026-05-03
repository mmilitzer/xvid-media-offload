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

// Scan discovers offload candidates by looking for marker files one level
// below each scan root, deriving candidate file paths directly from the
// marker's RewriteRule patterns, and checking those specific files.
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

	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("reading scan root %s: %w", root, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirPath := filepath.Join(root, entry.Name())
		markerPath := filepath.Join(dirPath, cfg.MarkerFilename)

		mInfo, err := marker.Parse(markerPath)
		if err != nil {
			res.Errors++
			continue
		}

		res.ManagedFolders++
		if !mInfo.Valid {
			res.InvalidMarkers++
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
			relToRoot := filepath.ToSlash(filepath.Join(entry.Name(), relPath))
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
				continue
			}

			// Check for .offloaded sibling.
			if _, err := os.Stat(absPath + ".offloaded"); err == nil {
				res.SkippedOffloaded++
				continue
			}

			// Check minimum age.
			age := time.Since(fileInfo.ModTime())
			if age < cfg.MinimumAge {
				res.SkippedTooYoung++
				continue
			}

			remoteID := mInfo.FileIDByRel[pattern]

			res.Candidates = append(res.Candidates, Candidate{
				Path:       absPath,
				RelPath:    relToRoot,
				Size:       fileInfo.Size(),
				ModTime:    fileInfo.ModTime(),
				Age:        age,
				RemoteID:   remoteID,
				Glob:       matchedGlob,
				MarkerPath: markerPath,
			})
			res.TotalCandidateSize += fileInfo.Size()
		}
	}

	return nil
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
