package shrink

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
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
	Candidates            []scanner.Candidate
	Offloaded             []OffloadInfo
	ManagedFolders        int
	ValidMarkers          int
	InvalidMarkers        int
	SkippedOffloaded      int
	SkippedSparse         int
	SkippedTooYoung       int
	SkippedMissingRemote  int
	SkippedTooSmall       int
	SkippedNoAutograph    int
	SkippedOwnerMismatch  int
	Errors                int
	TotalCandidateSize    int64
	BytesSaved            int64
	SkippedDetails        []scanner.SkipInfo
	ErrorDetails          []string
}

const offloadedMarkerContent = `This media file is stored on Xvid MediaHub cloud storage.
The local file is still present for CMS compatibility but it has been truncated with most disk space freed.
Delete this .offloaded file to initiate restoring the full local copy.
`

const sparseTmpSuffix = ".sparse-tmp."

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
			UID:        f.UID,
			GID:        f.GID,
			Mode:       f.Mode,
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

		// Enforce require-daemon-owner in shrink path as well (covers RunFromAll).
		if cfg.OwnershipPolicy == config.OwnershipPolicyRequireDaemonOwner {
			if c.UID != os.Geteuid() {
				res.SkippedOwnerMismatch++
				if cfg.Verbose {
					res.SkippedDetails = append(res.SkippedDetails, scanner.SkipInfo{
						Path:       c.Path,
						MarkerPath: c.MarkerPath,
						Reason:     "owner mismatch",
					})
				}
				continue
			}
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

		// Store original timestamps before modification.
		origAtime, origMtime, err := getFileTimestamps(c.Path)
		if err != nil {
			res.Errors++
			msg := fmt.Sprintf("%s: stat before offload failed: %v", c.Path, err)
			res.ErrorDetails = append(res.ErrorDetails, msg)
			if cfg.Verbose {
				fmt.Fprintln(os.Stderr, msg)
			}
			continue
		}

		ownedByDaemon := c.UID == os.Geteuid()
		useReplace := cfg.OwnershipPolicy == config.OwnershipPolicyReplaceWithDaemonOwnedSparse && !ownedByDaemon

		var markerPath string
		if useReplace {
			markerPath, err = replaceFileWithSparseCopy(c, cfg, origAtime, origMtime)
			if err != nil {
				res.Errors++
				msg := fmt.Sprintf("%s: sparse replacement failed: %v", c.Path, err)
				res.ErrorDetails = append(res.ErrorDetails, msg)
				if cfg.Verbose {
					fmt.Fprintln(os.Stderr, msg)
				}
				continue
			}
		} else {
			// Standard hole-punch strategy.
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
			markerPath = c.Path + ".offloaded"
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
				allowMtimeFailure := cfg.OwnershipPolicy == config.OwnershipPolicyAllowOwnerMismatch && !ownedByDaemon
				msg := fmt.Sprintf("%s: failed to restore original timestamps: %v", c.Path, err)
				if allowMtimeFailure {
					res.ErrorDetails = append(res.ErrorDetails, msg)
					if cfg.Verbose {
						fmt.Fprintln(os.Stderr, msg)
					}
				} else {
					res.Errors++
					res.ErrorDetails = append(res.ErrorDetails, msg)
					if cfg.Verbose {
						fmt.Fprintln(os.Stderr, msg)
					}
				}
			} else {
				// Verify mtime was actually restored (allow filesystem precision differences).
				var restoredStat unix.Stat_t
				if err := unix.Stat(c.Path, &restoredStat); err != nil {
					res.Errors++
					msg := fmt.Sprintf("%s: unix stat after chtimes failed: %v", c.Path, err)
					res.ErrorDetails = append(res.ErrorDetails, msg)
					if cfg.Verbose {
						fmt.Fprintln(os.Stderr, msg)
					}
				} else {
					restoredMtime := time.Unix(restoredStat.Mtim.Sec, restoredStat.Mtim.Nsec)
					if restoredMtime.Sub(origMtime).Abs() > time.Second {
						allowMtimeFailure := cfg.OwnershipPolicy == config.OwnershipPolicyAllowOwnerMismatch && !ownedByDaemon
						msg := fmt.Sprintf("%s: mtime not restored after offload: expected %v, got %v", c.Path, origMtime, restoredMtime)
						if allowMtimeFailure {
							res.ErrorDetails = append(res.ErrorDetails, msg)
							if cfg.Verbose {
								fmt.Fprintln(os.Stderr, msg)
							}
						} else {
							res.Errors++
							res.ErrorDetails = append(res.ErrorDetails, msg)
							if cfg.Verbose {
								fmt.Fprintln(os.Stderr, msg)
							}
						}
					}
				}
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

// replaceFileWithSparseCopy implements the replace-with-daemon-owned-sparse-copy
// strategy for files not owned by the daemon user.
func replaceFileWithSparseCopy(c scanner.Candidate, cfg *config.Config, origAtime, origMtime time.Time) (string, error) {
	// 1. stat original MP4
	var origStat unix.Stat_t
	if err := unix.Stat(c.Path, &origStat); err != nil {
		return "", fmt.Errorf("stat original file: %w", err)
	}

	// 2. reject non-regular files
	if origStat.Mode&unix.S_IFREG == 0 {
		return "", fmt.Errorf("original file is not a regular file")
	}

	// 3. reject hardlinked files with link count > 1
	if origStat.Nlink > 1 {
		return "", fmt.Errorf("hardlinked files with link count > 1 are not supported")
	}

	// 4. create temp sparse replacement with unique name
	tempPath := c.Path + sparseTmpSuffix + uuid.NewString()
	tempFile, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		if tempFile != nil {
			tempFile.Close()
		}
	}()

	// 5. copy only keep_prefix_bytes from original to temp
	srcFile, err := os.Open(c.Path)
	if err != nil {
		return "", fmt.Errorf("open original file: %w", err)
	}
	_, err = io.CopyN(tempFile, srcFile, cfg.KeepPrefixBytes)
	srcFile.Close()
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("copy prefix to temp: %w", err)
	}

	// 6. ftruncate temp to original logical size
	if err := tempFile.Truncate(c.Size); err != nil {
		return "", fmt.Errorf("truncate temp to original size: %w", err)
	}

	// 7. chmod temp to original mode
	if err := tempFile.Chmod(os.FileMode(origStat.Mode) & os.ModePerm); err != nil {
		return "", fmt.Errorf("chmod temp file: %w", err)
	}

	// Close before rename.
	if err := tempFile.Close(); err != nil {
		return "", fmt.Errorf("close temp file: %w", err)
	}
	tempFile = nil

	// 8. attempt chgrp to maintain group ownership if appropriate, but do not require it
	if c.GID != os.Getgid() {
		_ = os.Chown(tempPath, os.Getuid(), c.GID)
	}

	// 9. restore original mtime on temp
	if err := os.Chtimes(tempPath, origAtime, origMtime); err != nil {
		return "", fmt.Errorf("restore original mtime on temp: %w", err)
	}

	// 10. fsync temp
	f, err := os.Open(tempPath)
	if err != nil {
		return "", fmt.Errorf("open temp for fsync: %w", err)
	}
	if err := unix.Fsync(int(f.Fd())); err != nil {
		f.Close()
		return "", fmt.Errorf("fsync temp: %w", err)
	}
	f.Close()

	// 11. atomically rename temp over original path
	if err := os.Rename(tempPath, c.Path); err != nil {
		return "", fmt.Errorf("atomic rename: %w", err)
	}

	// 12. fsync parent directory
	dir := filepath.Dir(c.Path)
	dirFile, err := os.Open(dir)
	if err == nil {
		unix.Fsync(int(dirFile.Fd()))
		dirFile.Close()
	}

	// 13. create .offloaded marker
	markerPath := c.Path + ".offloaded"
	if err := cfg.CreateOffloadedMarker(c.Path, markerPath, []byte(offloadedMarkerContent)); err != nil {
		return "", fmt.Errorf("create offloaded marker: %w", err)
	}

	return markerPath, nil
}

// CleanStaleSparseTmp removes stale sparse replacement temp files next to an MP4.
// It looks in mp4Path's parent directory for entries matching
// <basename>.sparse-tmp.* and deletes them.
func CleanStaleSparseTmp(mp4Path string) {
	dir := filepath.Dir(mp4Path)
	base := filepath.Base(mp4Path)
	prefix := base + sparseTmpSuffix

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
