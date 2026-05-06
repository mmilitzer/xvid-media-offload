package shrink

import (
	"fmt"
	"os"
	"time"

	"github.com/mmilitzer/xvid-media-offload/pkg/config"
	"github.com/mmilitzer/xvid-media-offload/pkg/db"
	"github.com/mmilitzer/xvid-media-offload/pkg/punch"
	"github.com/mmilitzer/xvid-media-offload/pkg/scanner"
	"github.com/mmilitzer/xvid-media-offload/pkg/sparse"
	"golang.org/x/sys/unix"
)

// OffloadInfo holds details about a successfully offloaded file.
type OffloadInfo struct {
	Path            string
	RemoteID        string
	Autograph       int
	OriginalSize    int64
	AllocatedBefore int64
	AllocatedAfter  int64
	MarkerPath      string
	DBStatus        string
}

// Result holds the outcome of a shrink run.
type Result struct {
	Candidates           []scanner.Candidate
	Offloaded            []OffloadInfo
	ManagedFolders       int
	ValidMarkers         int
	InvalidMarkers       int
	SkippedOffloaded     int
	SkippedSparse        int
	SkippedTooYoung      int
	SkippedMissingRemote int
	SkippedTooSmall      int
	SkippedNoAutograph   int
	Errors               int
	TotalCandidateSize   int64
	BytesSaved           int64
	SkippedDetails       []scanner.SkipInfo
	ErrorDetails         []string
}

const offloadedMarkerContent = `This media file is stored on Xvid MediaHub cloud storage.
The local file is still present for CMS compatibility but it has been truncated with most disk space freed.
Delete this .offloaded file to initiate restoring the full local copy.
`

// Run scans for candidates and offloads them according to the configuration.
// If dryRun is true, no files are modified.
func Run(cfg *config.Config, dryRun bool, database *db.DB) (*Result, error) {
	scanRes, err := scanner.Scan(cfg)
	if err != nil {
		return nil, fmt.Errorf("scanning: %w", err)
	}

	res := &Result{
		ManagedFolders:       scanRes.ManagedFolders,
		ValidMarkers:         scanRes.ValidMarkers,
		InvalidMarkers:       scanRes.InvalidMarkers,
		SkippedOffloaded:     scanRes.SkippedOffloaded,
		SkippedTooYoung:      scanRes.SkippedTooYoung,
		SkippedMissingRemote: scanRes.SkippedMissingRemote,
		Errors:               scanRes.Errors,
		SkippedDetails:       scanRes.SkippedDetails,
		ErrorDetails:         scanRes.ErrorDetails,
	}

	return processCandidates(cfg, dryRun, database, scanRes.Candidates, res)
}

// RunFromAll performs shrink processing using a pre-computed ScanAll result.
// It derives shrink candidates from the ScanResult and applies the same
// shrink-specific checks as Run, avoiding a second filesystem walk.
func RunFromAll(cfg *config.Config, dryRun bool, database *db.DB, all *scanner.ScanResult) (*Result, error) {
	var candidates []scanner.Candidate
	var skippedOffloaded, skippedTooYoung, skippedMissingRemote int

	for _, f := range all.Files {
		if f.Size < 0 {
			skippedMissingRemote++
			continue
		}
		if f.HasOffloadedMarker {
			skippedOffloaded++
			continue
		}
		if f.Age < cfg.MinimumAge.Duration() {
			skippedTooYoung++
			continue
		}
		candidates = append(candidates, scanner.Candidate{
			Path:       f.Path,
			RelPath:    f.RelPath,
			Size:       f.Size,
			ModTime:    f.ModTime,
			Age:        f.Age,
			RemoteID:   f.RemoteID,
			Autograph:  f.Autograph,
			Glob:       f.Glob,
			MarkerPath: f.MarkerPath,
		})
	}

	res := &Result{
		ManagedFolders:       all.ManagedFolders,
		ValidMarkers:         all.ValidMarkers,
		InvalidMarkers:       all.InvalidMarkers,
		SkippedOffloaded:     skippedOffloaded,
		SkippedTooYoung:      skippedTooYoung,
		SkippedMissingRemote: skippedMissingRemote,
		Errors:               all.Errors,
		ErrorDetails:         all.ErrorDetails,
	}

	return processCandidates(cfg, dryRun, database, candidates, res)
}

func getFileTimestamps(path string) (atime, mtime time.Time, err error) {
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return time.Time{}, time.Time{}, err
	}
	atime = time.Unix(stat.Atim.Sec, stat.Atim.Nsec)
	mtime = time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec)
	return atime, mtime, nil
}

func processCandidates(cfg *config.Config, dryRun bool, database *db.DB, candidates []scanner.Candidate, res *Result) (*Result, error) {
	for _, c := range candidates {
		// Skip files that are too small.
		if c.Size <= 2*cfg.KeepPrefixBytes {
			res.SkippedTooSmall++
			if cfg.Verbose {
				res.SkippedDetails = append(res.SkippedDetails, scanner.SkipInfo{
					Path:       c.Path,
					MarkerPath: c.MarkerPath,
					Reason:     "too small",
				})
			}
			continue
		}

		// Skip files with no autograph value.
		if c.Autograph < 0 {
			res.SkippedNoAutograph++
			if cfg.Verbose {
				res.SkippedDetails = append(res.SkippedDetails, scanner.SkipInfo{
					Path:       c.Path,
					MarkerPath: c.MarkerPath,
					Reason:     "no autograph value",
				})
			}
			continue
		}

		// Skip files that already appear sparse.
		isSparse, err := sparse.IsSparse(c.Path)
		if err != nil {
			res.Errors++
			msg := fmt.Sprintf("%s: sparse check failed: %v", c.Path, err)
			res.ErrorDetails = append(res.ErrorDetails, msg)
			if cfg.Verbose {
				fmt.Fprintln(os.Stderr, msg)
			}
			continue
		}
		if isSparse {
			res.SkippedSparse++
			if cfg.Verbose {
				res.SkippedDetails = append(res.SkippedDetails, scanner.SkipInfo{
					Path:       c.Path,
					MarkerPath: c.MarkerPath,
					Reason:     "already sparse",
				})
			}
			continue
		}

		// File passes all shrink-specific checks — it is a real candidate.
		res.Candidates = append(res.Candidates, c)
		res.TotalCandidateSize += c.Size

		if dryRun {
			continue
		}

		// Apply mode: perform offload.
		allocatedBefore, err := sparse.AllocatedBytes(c.Path)
		if err != nil {
			res.Errors++
			msg := fmt.Sprintf("%s: allocated bytes check failed: %v", c.Path, err)
			res.ErrorDetails = append(res.ErrorDetails, msg)
			if cfg.Verbose {
				fmt.Fprintln(os.Stderr, msg)
			}
			continue
		}

		// Store original timestamps before hole punching.
		origAtime, origMtime, err := getFileTimestamps(c.Path)
		if err != nil {
			res.Errors++
			msg := fmt.Sprintf("%s: stat before punch failed: %v", c.Path, err)
			res.ErrorDetails = append(res.ErrorDetails, msg)
			if cfg.Verbose {
				fmt.Fprintln(os.Stderr, msg)
			}
			continue
		}

		err = punch.PunchHole(c.Path, cfg.KeepPrefixBytes)
		if err != nil {
			res.Errors++
			msg := fmt.Sprintf("%s: hole punch failed: %v", c.Path, err)
			res.ErrorDetails = append(res.ErrorDetails, msg)
			if cfg.Verbose {
				fmt.Fprintln(os.Stderr, msg)
			}
			continue
		}

		// Verify logical size is unchanged.
		fi, err := os.Stat(c.Path)
		if err != nil {
			res.Errors++
			msg := fmt.Sprintf("%s: stat after punch failed: %v", c.Path, err)
			res.ErrorDetails = append(res.ErrorDetails, msg)
			if cfg.Verbose {
				fmt.Fprintln(os.Stderr, msg)
			}
			continue
		}
		if fi.Size() != c.Size {
			res.Errors++
			msg := fmt.Sprintf("%s: logical size changed after punch: %d != %d", c.Path, fi.Size(), c.Size)
			res.ErrorDetails = append(res.ErrorDetails, msg)
			if cfg.Verbose {
				fmt.Fprintln(os.Stderr, msg)
			}
			continue
		}

		// Verify file is now sparse.
		isSparseAfter, err := sparse.IsSparse(c.Path)
		if err != nil {
			res.Errors++
			msg := fmt.Sprintf("%s: sparse check after punch failed: %v", c.Path, err)
			res.ErrorDetails = append(res.ErrorDetails, msg)
			if cfg.Verbose {
				fmt.Fprintln(os.Stderr, msg)
			}
			continue
		}
		if !isSparseAfter {
			res.Errors++
			msg := fmt.Sprintf("%s: file not sparse after punch", c.Path)
			res.ErrorDetails = append(res.ErrorDetails, msg)
			if cfg.Verbose {
				fmt.Fprintln(os.Stderr, msg)
			}
			continue
		}

		// Create .offloaded marker.
		markerPath := c.Path + ".offloaded"
		err = cfg.CreateOffloadedMarker(c.Path, markerPath, []byte(offloadedMarkerContent))
		if err != nil {
			res.Errors++
			msg := fmt.Sprintf("%s: failed to create offloaded marker: %v", c.Path, err)
			res.ErrorDetails = append(res.ErrorDetails, msg)
			if cfg.Verbose {
				fmt.Fprintln(os.Stderr, msg)
			}
			continue
		}

		// Restore original timestamps so the MP4 keeps its original modified time.
		if err := os.Chtimes(c.Path, origAtime, origMtime); err != nil {
			res.Errors++
			msg := fmt.Sprintf("%s: failed to restore original timestamps: %v", c.Path, err)
			res.ErrorDetails = append(res.ErrorDetails, msg)
			if cfg.Verbose {
				fmt.Fprintln(os.Stderr, msg)
			}
		}

		// Write DB record if configured.
		dbStatus := "skipped (no database configured)"
		if database != nil {
			err = database.UpsertOffloadedFile(c.Path, c.RemoteID, c.Autograph, c.Size, time.Now())
			if err != nil {
				dbStatus = fmt.Sprintf("error: %v", err)
				res.ErrorDetails = append(res.ErrorDetails, fmt.Sprintf("%s: DB write failed: %v", c.Path, err))
				if cfg.Verbose {
					fmt.Fprintf(os.Stderr, "%s: DB write failed: %v\n", c.Path, err)
				}
			} else {
				dbStatus = "ok"
			}
		}

		allocatedAfter, _ := sparse.AllocatedBytes(c.Path)
		bytesSaved := allocatedBefore - allocatedAfter

		res.Offloaded = append(res.Offloaded, OffloadInfo{
			Path:            c.Path,
			RemoteID:        c.RemoteID,
			Autograph:       c.Autograph,
			OriginalSize:    c.Size,
			AllocatedBefore: allocatedBefore,
			AllocatedAfter:  allocatedAfter,
			MarkerPath:      markerPath,
			DBStatus:        dbStatus,
		})
		res.BytesSaved += bytesSaved
	}

	return res, nil
}
