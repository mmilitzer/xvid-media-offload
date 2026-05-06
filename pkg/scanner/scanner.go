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
	"github.com/mmilitzer/xvid-media-offload/pkg/sparse"
	"golang.org/x/sys/unix"
)

// FileInfo represents a managed MP4 file found during scanning.
type FileInfo struct {
	Path               string
	RelPath            string
	Size               int64
	ModTime            time.Time
	Age                time.Duration
	RemoteID           string
	Autograph          int // -1 means not set, 0 or 1 means explicit value
	Glob               string
	MarkerPath         string
	ScanRoot           string
	IsSparse           bool
	HasOffloadedMarker bool
	UID                int
	GID                int
	Mode               os.FileMode
}

// Candidate represents a file that is eligible for offloading.
// Deprecated: use FileInfo directly for new code.
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
	UID        int
	GID        int
	Mode       os.FileMode
}

// SkipInfo records a file that was skipped during scanning.
type SkipInfo struct {
	Path       string
	MarkerPath string
	Reason     string
}

// Result holds the outcome of a scan.
type Result struct {
	Candidates            []Candidate
	ManagedFolders        int
	ValidMarkers          int
	InvalidMarkers        int
	SkippedOffloaded      int
	SkippedTooYoung       int
	SkippedMissingRemote  int
	SkippedOwnerMismatch  int
	Errors                int
	TotalCandidateSize    int64
	SkippedDetails        []SkipInfo
	ErrorDetails          []string
}

// ScanResult holds the outcome of a generalized scan.
type ScanResult struct {
	Files                []FileInfo
	ManagedFolders       int
	ValidMarkers         int
	InvalidMarkers       int
	SkippedMissingRemote int
	Errors               int
	ErrorDetails         []string
}

// Scan discovers offload candidates by looking for marker files at the
// configured depth below each scan root, deriving candidate file paths
// directly from the marker's RewriteRule patterns, and checking those
// specific files.
func Scan(cfg *config.Config) (*Result, error) {
	all, err := ScanAll(cfg)
	if err != nil {
		return nil, err
	}

	res := &Result{
		Candidates: make([]Candidate, 0),
	}

	res.ManagedFolders = all.ManagedFolders
	res.ValidMarkers = all.ValidMarkers
	res.InvalidMarkers = all.InvalidMarkers
	res.SkippedMissingRemote = all.SkippedMissingRemote
	res.Errors = all.Errors
	res.ErrorDetails = all.ErrorDetails

	for _, f := range all.Files {
		// Skip files that don't exist on disk.
		if f.Size < 0 {
			res.SkippedMissingRemote++
			if cfg.Verbose {
				res.SkippedDetails = append(res.SkippedDetails, SkipInfo{
					Path:       f.Path,
					MarkerPath: f.MarkerPath,
					Reason:     "file not found on disk",
				})
			}
			continue
		}

		// Skip already offloaded.
		if f.HasOffloadedMarker {
			res.SkippedOffloaded++
			if cfg.Verbose {
				res.SkippedDetails = append(res.SkippedDetails, SkipInfo{
					Path:       f.Path,
					MarkerPath: f.MarkerPath,
					Reason:     "already offloaded",
				})
			}
			continue
		}

		// Skip too young.
		if f.Age < cfg.MinimumAge.Duration() {
			res.SkippedTooYoung++
			if cfg.Verbose {
				res.SkippedDetails = append(res.SkippedDetails, SkipInfo{
					Path:       f.Path,
					MarkerPath: f.MarkerPath,
					Reason:     "too young",
				})
			}
			continue
		}

		// Skip files not owned by the daemon user when require-daemon-owner is active.
		if cfg.OwnershipPolicy == config.OwnershipPolicyRequireDaemonOwner {
			if f.UID != os.Geteuid() {
				res.SkippedOwnerMismatch++
				if cfg.Verbose {
					res.SkippedDetails = append(res.SkippedDetails, SkipInfo{
						Path:       f.Path,
						MarkerPath: f.MarkerPath,
						Reason:     "owner mismatch",
					})
				}
				continue
			}
		}

		res.Candidates = append(res.Candidates, Candidate{
			Path:       f.Path,
			RelPath:    f.RelPath,
			Size:       f.Size,
			ModTime:    f.ModTime,
			Age:        f.Age,
			RemoteID:   f.RemoteID,
			Autograph:  f.Autograph,
			Glob:       f.Glob,
			MarkerPath: f.MarkerPath,
			UID:        f.UID,
			GID:        f.GID,
			Mode:       f.Mode,
		})
		res.TotalCandidateSize += f.Size
	}

	return res, nil
}

// ScanAll performs a generalized scan and returns all managed MP4 files
// with their full metadata, regardless of whether they are shrink or restore
// candidates. It is the foundation for both shrink and restore modes.
func ScanAll(cfg *config.Config) (*ScanResult, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	res := &ScanResult{
		Files: make([]FileInfo, 0),
	}

	for _, root := range cfg.ScanRoots {
		if err := scanRootAll(root, cfg, res); err != nil {
			return nil, fmt.Errorf("scanning root %s: %w", root, err)
		}
	}

	return res, nil
}

// ScanForRestore performs a scan and returns only files that are candidates
// for restoration: sparse MP4 files without an .offloaded marker.
func ScanForRestore(cfg *config.Config) (*ScanResult, error) {
	all, err := ScanAll(cfg)
	if err != nil {
		return nil, err
	}

	restoreRes := &ScanResult{
		ManagedFolders:       all.ManagedFolders,
		ValidMarkers:         all.ValidMarkers,
		InvalidMarkers:       all.InvalidMarkers,
		SkippedMissingRemote: all.SkippedMissingRemote,
		Errors:               all.Errors,
		ErrorDetails:         all.ErrorDetails,
		Files:                make([]FileInfo, 0),
	}

	for _, f := range all.Files {
		// Skip files that don't exist on disk.
		if f.Size < 0 {
			restoreRes.SkippedMissingRemote++
			continue
		}

		// Restore candidates must be sparse and not have an .offloaded marker.
		if !f.IsSparse {
			continue
		}
		if f.HasOffloadedMarker {
			continue
		}

		restoreRes.Files = append(restoreRes.Files, f)
	}

	return restoreRes, nil
}

func scanRootAll(root string, cfg *config.Config, res *ScanResult) error {
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

			// Clean up any stale sparse replacement temp files next to this MP4.
			cleanStaleSparseTmp(absPath)

			fileInfo, err := os.Stat(absPath)
			var size int64 = -1
			var modTime time.Time
			var age time.Duration
			isSparse := false
			hasOffloaded := false
			var uid, gid int
			var mode os.FileMode

			if err == nil {
				size = fileInfo.Size()
				modTime = fileInfo.ModTime()
				age = time.Since(modTime)
				isSparse, _ = sparse.IsSparse(absPath)
				if _, err := os.Stat(absPath + ".offloaded"); err == nil {
					hasOffloaded = true
				}
				mode = fileInfo.Mode()
				// Use unix.Stat to get ownership info reliably.
				var stat unix.Stat_t
				if unixStatErr := unix.Stat(absPath, &stat); unixStatErr == nil {
					uid = int(stat.Uid)
					gid = int(stat.Gid)
				}
			}

			remoteID := mInfo.FileIDByRel[pattern]
			autograph := -1
			if a, ok := mInfo.MatchAutograph(relPath); ok {
				autograph = a
			}

			res.Files = append(res.Files, FileInfo{
				Path:               absPath,
				RelPath:            relToRoot,
				Size:               size,
				ModTime:            modTime,
				Age:                age,
				RemoteID:           remoteID,
				Autograph:          autograph,
				Glob:               matchedGlob,
				MarkerPath:         markerPath,
				ScanRoot:           root,
				IsSparse:           isSparse,
				HasOffloadedMarker: hasOffloaded,
				UID:                uid,
				GID:                gid,
				Mode:               mode,
			})
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

// cleanStaleSparseTmp removes stale sparse replacement temp files next to an MP4.
// It looks in mp4Path's parent directory for entries matching
// <basename>.sparse-tmp.* and deletes them.
func cleanStaleSparseTmp(mp4Path string) {
	dir := filepath.Dir(mp4Path)
	base := filepath.Base(mp4Path)
	prefix := base + ".sparse-tmp."

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
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
