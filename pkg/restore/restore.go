package restore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mmilitzer/xvid-media-offload/pkg/config"
	"github.com/mmilitzer/xvid-media-offload/pkg/credentials"
	"github.com/mmilitzer/xvid-media-offload/pkg/db"
	"github.com/mmilitzer/xvid-media-offload/pkg/download"
	"github.com/mmilitzer/xvid-media-offload/pkg/scanner"
	"golang.org/x/sys/unix"
)

const offloadedMarkerContent = `This media file is stored on Xvid MediaHub cloud storage.
The local file is still present for CMS compatibility but it has been truncated with most disk space freed.
Delete this .offloaded file to initiate restoring the full local copy.
`

// RestoreInfo holds details about a successfully restored file.
type RestoreInfo struct {
	Path       string
	RemoteID   string
	Autograph  int
	Size       int64
	MarkerPath string
}

// Result holds the outcome of a restore run.
type Result struct {
	Candidates         int
	Queued             int
	Restored           []RestoreInfo
	Failed             int
	SkippedNoRemoteID  int
	SkippedNoCreds     int
	SkippedNoSpace     int
	Errors             int
	ErrorDetails       []string
}

// Job represents a single restore task.
type Job struct {
	File        scanner.FileInfo
	Credentials *credentials.Credentials
}

// Run scans for restore candidates and restores them using a worker pool.
// If dryRun is true, no files are modified or downloaded.
func Run(cfg *config.Config, dryRun bool, workers int, database *db.DB, downloader download.Downloader) (*Result, error) {
	if workers <= 0 {
		workers = 4
	}

	scanRes, err := scanForRestore(cfg)
	if err != nil {
		return nil, fmt.Errorf("scanning for restore: %w", err)
	}

	res := &Result{
		Candidates: len(scanRes.Files),
	}

	// Pre-resolve remote IDs and credentials, build job queue.
	// Credentials are cached per scan root to avoid repeated filesystem walks.
	credCache := make(map[string]*credentials.Credentials)
	var jobs []Job
	for _, f := range scanRes.Files {
		remoteID := f.RemoteID

		// Fallback to DB if no remote ID from marker.
		if remoteID == "" && database != nil {
			dbRemoteID, _, _, _, dbErr := database.GetOffloadedFile(f.Path)
			if dbErr == nil {
				remoteID = dbRemoteID
			}
		}

		if remoteID == "" {
			res.SkippedNoRemoteID++
			msg := fmt.Sprintf("%s: no remote file id (marker or DB)", f.Path)
			res.ErrorDetails = append(res.ErrorDetails, msg)
			if cfg.Verbose {
				fmt.Fprintln(os.Stderr, msg)
			}
			continue
		}

		if dryRun {
			res.Queued++
			continue
		}

		// Find credentials for this file's scan root (cached).
		creds, ok := credCache[f.ScanRoot]
		if !ok {
			var credErr error
			creds, _, credErr = credentials.FindForPath(f.ScanRoot)
			if credErr != nil {
				res.SkippedNoCreds++
				msg := fmt.Sprintf("%s: no credentials found: %v", f.Path, credErr)
				res.ErrorDetails = append(res.ErrorDetails, msg)
				if cfg.Verbose {
					fmt.Fprintln(os.Stderr, msg)
				}
				// Cache a nil marker so we don't retry this scan root.
				credCache[f.ScanRoot] = nil
				continue
			}
			credCache[f.ScanRoot] = creds
		}
		if creds == nil {
			res.SkippedNoCreds++
			continue
		}

		jobs = append(jobs, Job{
			File:        f,
			Credentials: creds,
		})
		res.Queued++
	}

	if dryRun || len(jobs) == 0 {
		return res, nil
	}

	// Worker pool.
	var wg sync.WaitGroup
	jobCh := make(chan Job, len(jobs))
	var mu sync.Mutex

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				if err := restoreJob(job, cfg, database, downloader, res, &mu); err != nil {
					mu.Lock()
					res.Failed++
					msg := fmt.Sprintf("%s: restore failed: %v", job.File.Path, err)
					res.ErrorDetails = append(res.ErrorDetails, msg)
					if cfg.Verbose {
						fmt.Fprintln(os.Stderr, msg)
					}
					mu.Unlock()
				}
			}
		}()
	}

	for _, job := range jobs {
		jobCh <- job
	}
	close(jobCh)
	wg.Wait()

	return res, nil
}

// RestoreOne performs a single-file restore and returns details about the restored file.
func RestoreOne(ctx context.Context, job Job, cfg *config.Config, database *db.DB, downloader download.Downloader) (RestoreInfo, error) {
	return restoreOne(ctx, job, cfg, database, downloader)
}

func restoreOne(ctx context.Context, job Job, cfg *config.Config, database *db.DB, downloader download.Downloader) (RestoreInfo, error) {
	f := job.File

	// Resolve remote ID and autograph again inside worker.
	remoteID := f.RemoteID
	autograph := f.Autograph
	if remoteID == "" && database != nil {
		dbRemoteID, dbAutograph, _, _, dbErr := database.GetOffloadedFile(f.Path)
		if dbErr == nil {
			remoteID = dbRemoteID
			autograph = dbAutograph
		}
	}
	if remoteID == "" {
		return RestoreInfo{}, fmt.Errorf("no remote file id")
	}

	creds := job.Credentials
	if creds == nil {
		return RestoreInfo{}, fmt.Errorf("no credentials available")
	}

	// Check disk space.
	enough, avail, err := checkDiskSpace(f.Path, f.Size)
	if err != nil {
		return RestoreInfo{}, fmt.Errorf("disk space check: %w", err)
	}
	if !enough {
		// Recreate .offloaded marker and skip.
		err = os.WriteFile(f.Path+".offloaded", []byte(offloadedMarkerContent), 0644)
		if err != nil {
			return RestoreInfo{}, fmt.Errorf("not enough space (%d bytes available) and failed to recreate marker: %w", avail, err)
		}
		return RestoreInfo{}, fmt.Errorf("not enough disk space (%d bytes available)", avail)
	}

	// Sign download URL.
	signer := &download.Signer{
		ClientID:     creds.ClientID,
		ClientSecret: creds.ClientSecret,
	}
	signedURL, err := signer.SignURL(remoteID, autograph == 1)
	if err != nil {
		return RestoreInfo{}, fmt.Errorf("signing URL: %w", err)
	}

	// Before downloading, clean up any stale temp files left by previous
	// crashed or interrupted restore attempts.
	baseName := filepath.Base(f.Path)
	dir := filepath.Dir(f.Path)
	if entries, readErr := os.ReadDir(dir); readErr == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), baseName+".restore.") && strings.HasSuffix(e.Name(), ".tmp") {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}

	// Download to temp file.
	tempPath := f.Path + ".restore." + uuid.NewString() + ".tmp"
	err = downloader.DownloadContext(ctx, signedURL, tempPath)
	if err != nil {
		os.Remove(tempPath)
		return RestoreInfo{}, fmt.Errorf("download: %w", err)
	}

	// Verify size if known.
	if f.Size > 0 {
		fi, err := os.Stat(tempPath)
		if err != nil {
			os.Remove(tempPath)
			return RestoreInfo{}, fmt.Errorf("stat temp file: %w", err)
		}
		if fi.Size() != f.Size {
			os.Remove(tempPath)
			return RestoreInfo{}, fmt.Errorf("size mismatch: expected %d, got %d", f.Size, fi.Size())
		}
	}

	// Atomic rename.
	err = os.Rename(tempPath, f.Path)
	if err != nil {
		os.Remove(tempPath)
		return RestoreInfo{}, fmt.Errorf("atomic rename: %w", err)
	}

	// Fsync directory.
	dir = filepath.Dir(f.Path)
	dirFile, err := os.Open(dir)
	if err == nil {
		unix.Fsync(int(dirFile.Fd()))
		dirFile.Close()
	}

	// Update mtime to now so it is not immediately offloaded again.
	now := time.Now()
	os.Chtimes(f.Path, now, now)

	// Remove DB entry if present.
	if database != nil {
		_ = database.DeleteOffloadedFile(f.Path)
	}

	return RestoreInfo{
		Path:       f.Path,
		RemoteID:   remoteID,
		Autograph:  autograph,
		Size:       f.Size,
		MarkerPath: f.MarkerPath,
	}, nil
}

func restoreJob(job Job, cfg *config.Config, database *db.DB, downloader download.Downloader, res *Result, mu *sync.Mutex) error {
	info, err := restoreOne(context.Background(), job, cfg, database, downloader)
	if err != nil {
		if strings.Contains(err.Error(), "not enough disk space") {
			mu.Lock()
			res.SkippedNoSpace++
			mu.Unlock()
			return nil
		}
		return err
	}
	mu.Lock()
	res.Restored = append(res.Restored, info)
	mu.Unlock()
	return nil
}

var checkDiskSpace = hasEnoughSpace

var scanForRestore = scanner.ScanForRestore

func hasEnoughSpace(path string, needed int64) (bool, int64, error) {
	var stat unix.Statfs_t
	err := unix.Statfs(filepath.Dir(path), &stat)
	if err != nil {
		return false, 0, fmt.Errorf("statfs: %w", err)
	}
	avail := int64(stat.Bavail) * int64(stat.Bsize)
	// Require a little headroom beyond the file size itself.
	return avail > needed+1024*1024, avail, nil
}
