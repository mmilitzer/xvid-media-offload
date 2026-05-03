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
		Candidates:           scanRes.Candidates,
		ManagedFolders:       scanRes.ManagedFolders,
		ValidMarkers:         scanRes.ValidMarkers,
		InvalidMarkers:       scanRes.InvalidMarkers,
		SkippedOffloaded:     scanRes.SkippedOffloaded,
		SkippedTooYoung:      scanRes.SkippedTooYoung,
		SkippedMissingRemote: scanRes.SkippedMissingRemote,
		Errors:               scanRes.Errors,
		TotalCandidateSize:   scanRes.TotalCandidateSize,
		SkippedDetails:       scanRes.SkippedDetails,
		ErrorDetails:         scanRes.ErrorDetails,
	}

	for _, c := range scanRes.Candidates {
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

		if dryRun {
			res.TotalCandidateSize += c.Size
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
		err = os.WriteFile(markerPath, []byte(offloadedMarkerContent), 0644)
		if err != nil {
			res.Errors++
			msg := fmt.Sprintf("%s: failed to create offloaded marker: %v", c.Path, err)
			res.ErrorDetails = append(res.ErrorDetails, msg)
			if cfg.Verbose {
				fmt.Fprintln(os.Stderr, msg)
			}
			continue
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
