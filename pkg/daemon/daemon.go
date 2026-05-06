package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mmilitzer/xvid-media-offload/pkg/config"
	"github.com/mmilitzer/xvid-media-offload/pkg/credentials"
	"github.com/mmilitzer/xvid-media-offload/pkg/db"
	"github.com/mmilitzer/xvid-media-offload/pkg/download"
	"github.com/mmilitzer/xvid-media-offload/pkg/lockfile"
	"github.com/mmilitzer/xvid-media-offload/pkg/marker"
	"github.com/mmilitzer/xvid-media-offload/pkg/restore"
	"github.com/mmilitzer/xvid-media-offload/pkg/scanner"
	"github.com/mmilitzer/xvid-media-offload/pkg/shrink"
	"github.com/mmilitzer/xvid-media-offload/pkg/version"
)

// Daemon runs the media-offload service in continuous mode.
type Daemon struct {
	cfg        *config.Config
	dryRun     bool
	database   *db.DB
	downloader download.Downloader

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// producerWg tracks goroutines that can enqueue into the restore queue
	// (inotify reader, scan ticker callback). We wait for producers to stop
	// before closing the queue to avoid send-on-closed-channel panics.
	producerWg sync.WaitGroup

	// restoreCtx is cancelled on shutdown to abort active downloads.
	restoreCtx    context.Context
	restoreCancel context.CancelFunc

	lock *lockfile.Lock

	ticker *time.Ticker

	inotifyFd int
	watches   map[string]int // dirPath -> watch descriptor
	watchesMu sync.Mutex

	queue      chan restoreJob
	inFlight   map[string]bool
	inFlightMu sync.Mutex

	// overflowRescan signals that an inotify queue overflow occurred.
	overflowRescan chan struct{}
}

type restoreJob struct {
	Path       string
	RemoteID   string
	Autograph  int
	Size       int64
	MarkerPath string
	ScanRoot   string
}

// NewDaemon creates a new daemon instance.
func NewDaemon(cfg *config.Config, dryRun bool, database *db.DB, downloader download.Downloader) *Daemon {
	return &Daemon{
		cfg:            cfg,
		dryRun:         dryRun,
		database:       database,
		downloader:     downloader,
		inotifyFd:      -1,
		watches:        make(map[string]int),
		queue:          make(chan restoreJob, 100),
		inFlight:       make(map[string]bool),
		overflowRescan: make(chan struct{}, 1),
		// Initialize ctx so tests that call enqueueRestore directly don't panic.
		ctx: context.Background(),
	}
}

// Start begins the daemon and blocks until shutdown.
func (d *Daemon) Start() error {
	// Acquire single-instance lock before anything else.
	lock, err := lockfile.Acquire(d.cfg.LockFile)
	if err != nil {
		return err
	}
	d.lock = lock

	d.ctx, d.cancel = context.WithCancel(context.Background())
	defer d.cancel()

	d.restoreCtx, d.restoreCancel = context.WithCancel(context.Background())
	defer d.restoreCancel()

	// Signal handling.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	log.Printf("daemon starting — %s", version.String())

	// Start restore workers FIRST so DB reconciliation and scans can enqueue.
	for i := 0; i < d.cfg.RestoreWorkers; i++ {
		d.wg.Add(1)
		go d.restoreWorker()
	}

	// Start inotify reader if supported.
	if err := d.startInotify(); err != nil {
		log.Printf("inotify not available: %v", err)
	} else {
		d.producerWg.Add(1)
		go d.inotifyReader()
	}

	// DB reconciliation.
	if d.database != nil {
		log.Println("startup DB reconciliation beginning")
		if err := d.reconcileDB(); err != nil {
			log.Printf("DB reconciliation error: %v", err)
		}
		log.Println("startup DB reconciliation complete")
	}

	// Initial scan.
	d.runScan()

	// Periodic scans.
	d.ticker = time.NewTicker(d.cfg.ScanInterval.Duration())
	defer d.ticker.Stop()

	log.Printf("daemon running, scan interval: %v", d.cfg.ScanInterval.Duration())

	for {
		select {
		case <-sigCh:
			log.Println("daemon received shutdown signal")
			d.shutdown()
			return nil
		case <-d.ctx.Done():
			d.shutdown()
			return nil
		case <-d.ticker.C:
			d.runScan()
		case <-d.overflowRescan:
			log.Println("inotify queue overflow: scheduling full rescan")
			d.runScan()
		}
	}
}

// Stop initiates a clean shutdown of the daemon.
func (d *Daemon) Stop() {
	if d.cancel != nil {
		d.cancel()
	}
}

func (d *Daemon) shutdown() {
	log.Println("daemon shutting down")

	// 1. Cancel all contexts so producers and downloads stop.
	if d.restoreCancel != nil {
		d.restoreCancel()
	}
	if d.cancel != nil {
		d.cancel()
	}

	// 2. Stop the ticker so no new scans are scheduled.
	if d.ticker != nil {
		d.ticker.Stop()
	}

	// 3. Stop inotify so no new events are produced.
	d.stopInotify()

	// 4. Wait for all producers to finish before closing the queue.
	d.producerWg.Wait()

	// 5. Close the queue so workers exit.
	close(d.queue)

	// 6. Wait for workers to drain.
	d.wg.Wait()

	// 7. Close DB.
	if d.database != nil {
		d.database.Close()
	}

	// 8. Release the single-instance lock.
	if d.lock != nil {
		_ = d.lock.Release()
	}

	log.Println("daemon stopped")
}

func (d *Daemon) runScan() {
	log.Println("scan beginning")
	defer log.Println("scan complete")

	d.combinedScan()
}

// enqueueRestore adds a restore job if not already in flight and not shutting down.
func (d *Daemon) enqueueRestore(job restoreJob) bool {
	// Fast-path: if shutting down, don't enqueue.
	select {
	case <-d.ctx.Done():
		return false
	default:
	}

	d.inFlightMu.Lock()
	if d.inFlight[job.Path] {
		d.inFlightMu.Unlock()
		log.Printf("duplicate restore skipped (in flight): %s", job.Path)
		return false
	}
	d.inFlight[job.Path] = true
	d.inFlightMu.Unlock()

	select {
	case d.queue <- job:
		if d.dryRun {
			log.Printf("dry-run: would enqueue restore: %s", job.Path)
		} else {
			log.Printf("restore enqueued: %s", job.Path)
		}
		return true
	case <-d.ctx.Done():
		d.inFlightMu.Lock()
		delete(d.inFlight, job.Path)
		d.inFlightMu.Unlock()
		return false
	default:
		d.inFlightMu.Lock()
		delete(d.inFlight, job.Path)
		d.inFlightMu.Unlock()
		log.Printf("restore queue full, skipping: %s", job.Path)
		return false
	}
}

// enqueueRestoreBlocking adds a restore job, blocking until the queue has room
// or the context is cancelled. Used by DB reconciliation to avoid silently
// dropping jobs when the queue buffer is full.
func (d *Daemon) enqueueRestoreBlocking(ctx context.Context, job restoreJob) bool {
	d.inFlightMu.Lock()
	if d.inFlight[job.Path] {
		d.inFlightMu.Unlock()
		log.Printf("duplicate restore skipped (in flight): %s", job.Path)
		return false
	}
	d.inFlight[job.Path] = true
	d.inFlightMu.Unlock()

	select {
	case d.queue <- job:
		if d.dryRun {
			log.Printf("dry-run: would enqueue restore: %s", job.Path)
		} else {
			log.Printf("restore enqueued: %s", job.Path)
		}
		return true
	case <-ctx.Done():
		d.inFlightMu.Lock()
		delete(d.inFlight, job.Path)
		d.inFlightMu.Unlock()
		return false
	}
}

func (d *Daemon) restoreWorker() {
	defer d.wg.Done()
	for job := range d.queue {
		if d.dryRun {
			log.Printf("dry-run: would restore: %s", job.Path)
			d.inFlightMu.Lock()
			delete(d.inFlight, job.Path)
			d.inFlightMu.Unlock()
			continue
		}

		if err := d.runRestore(job); err != nil {
			log.Printf("restore failed: %s: %v", job.Path, err)
		} else {
			log.Printf("restore complete: %s", job.Path)
		}

		d.inFlightMu.Lock()
		delete(d.inFlight, job.Path)
		d.inFlightMu.Unlock()
	}
}

func (d *Daemon) runRestore(job restoreJob) error {
	creds, _, err := credentials.FindForPath(job.Path)
	if err != nil {
		return fmt.Errorf("no credentials: %w", err)
	}

	fileInfo := scanner.FileInfo{
		Path:       job.Path,
		Size:       job.Size,
		RemoteID:   job.RemoteID,
		Autograph:  job.Autograph,
		MarkerPath: job.MarkerPath,
		ScanRoot:   job.ScanRoot,
	}

	rjob := restore.Job{
		File:        fileInfo,
		Credentials: creds,
	}

	_, err = restore.RestoreOne(d.restoreCtx, rjob, d.cfg, d.database, d.downloader)
	if err != nil {
		// Clean up any partial temp file left behind by cancellation or failure.
		dir := filepath.Dir(job.Path)
		entries, readErr := os.ReadDir(dir)
		if readErr == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), filepath.Base(job.Path)+".restore.") && strings.HasSuffix(e.Name(), ".tmp") {
					_ = os.Remove(filepath.Join(dir, e.Name()))
				}
			}
		}
	}
	return err
}

// combinedScan performs a single filesystem walk and derives both shrink and
// restore candidates from it, then registers inotify watches for .offloaded markers.
func (d *Daemon) combinedScan() {
	all, err := scanner.ScanAll(d.cfg)
	if err != nil {
		log.Printf("combined scan error: %v", err)
		return
	}

	// Shrink processing from the same ScanAll result.
	shrinkRes, err := shrink.RunFromAll(d.cfg, d.dryRun, d.database, all)
	if err != nil {
		log.Printf("shrink error: %v", err)
	} else {
		if shrinkRes.Errors > 0 {
			log.Printf("shrink encountered %d error(s)", shrinkRes.Errors)
			for _, detail := range shrinkRes.ErrorDetails {
				log.Printf("shrink error: %s", detail)
			}
		}
		if !d.dryRun && len(shrinkRes.Offloaded) > 0 {
			log.Printf("shrink offloaded %d files", len(shrinkRes.Offloaded))
			// Register inotify watches for newly offloaded files immediately.
			for _, o := range shrinkRes.Offloaded {
				dir := filepath.Dir(o.Path)
				if err := d.addInotifyWatch(dir); err != nil {
					log.Printf("inotify watch error for newly offloaded %s: %v", dir, err)
				}
			}
		} else if d.dryRun && len(shrinkRes.Candidates) > 0 {
			log.Printf("dry-run: would shrink %d files", len(shrinkRes.Candidates))
		}
	}

	// Restore candidates and inotify watches derived from the same ScanAll result.
	restoreCount := 0
	for _, f := range all.Files {
		if f.Size < 0 {
			continue
		}
		if f.HasOffloadedMarker {
			dir := filepath.Dir(f.Path)
			if err := d.addInotifyWatch(dir); err != nil {
				log.Printf("inotify watch error for %s: %v", dir, err)
			}
			continue
		}
		if f.IsSparse {
			restoreCount++
			job := restoreJob{
				Path:       f.Path,
				RemoteID:   f.RemoteID,
				Autograph:  f.Autograph,
				Size:       f.Size,
				MarkerPath: f.MarkerPath,
				ScanRoot:   f.ScanRoot,
			}
			d.enqueueRestore(job)
		}
	}

	if restoreCount > 0 {
		log.Printf("restore scan found %d candidates", restoreCount)
	}
}

// findMarkerForPathWithin walks upward from the file's directory looking for
// the configured marker file, but never searches above scanRoot. It returns
// the marker path and the directory it was found in.
func findMarkerForPathWithin(path string, markerFilename string, scanRoot string) (markerPath string, markerDir string) {
	cleanPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", ""
	}
	cleanRoot, err := filepath.Abs(filepath.Clean(scanRoot))
	if err != nil {
		return "", ""
	}

	// Verify path is under scanRoot.
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return "", ""
	}
	if rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
		return "", ""
	}

	dir := filepath.Dir(cleanPath)
	searchDir := dir
	for {
		// Don't search above scanRoot.
		relSearch, err := filepath.Rel(cleanRoot, searchDir)
		if err != nil {
			return "", ""
		}
		if relSearch == ".." || filepath.IsAbs(relSearch) || strings.HasPrefix(relSearch, "..") {
			return "", ""
		}

		candidate := filepath.Join(searchDir, markerFilename)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, searchDir
		}
		parent := filepath.Dir(searchDir)
		if parent == searchDir {
			break
		}
		searchDir = parent
	}
	return "", ""
}

// resolveRestoreJob attempts to build a restoreJob for a file path by reading
// the marker file in its ancestor directories (bounded by the matching scan
// root). It falls back to the DB if the marker does not contain the file.
// If the path is outside all configured scan roots, no restore is triggered.
func (d *Daemon) resolveRestoreJob(path string) (restoreJob, bool) {
	scanRoot, ok := findScanRootForPath(path, d.cfg.ScanRoots)
	if !ok {
		// No active scan_root matches. Behave consistently with startup DB
		// reconciliation, which skips records outside scan roots entirely.
		return restoreJob{}, false
	}

	markerPath, markerDir := findMarkerForPathWithin(path, d.cfg.MarkerFilename, scanRoot)

	var remoteID string
	var autograph int = -1
	var size int64

	if markerPath != "" {
		if info, err := marker.Parse(markerPath); err == nil && info.Valid {
			relPath, _ := filepath.Rel(markerDir, path)
			relPath = filepath.ToSlash(relPath)
			if id, ok := info.MatchFileID(relPath); ok {
				remoteID = id
			}
			if a, ok := info.MatchAutograph(relPath); ok {
				autograph = a
			}
		}
	}

	if remoteID == "" && d.database != nil {
		if dbRemoteID, dbAutograph, dbSize, _, _, _, _, err := d.database.GetOffloadedFile(path); err == nil {
			remoteID = dbRemoteID
			autograph = dbAutograph
			size = dbSize
		}
	}

	if remoteID == "" {
		return restoreJob{}, false
	}

	if size == 0 {
		if fi, err := os.Stat(path); err == nil {
			size = fi.Size()
		}
	}

	return restoreJob{
		Path:       path,
		RemoteID:   remoteID,
		Autograph:  autograph,
		Size:       size,
		MarkerPath: markerPath,
		ScanRoot:   scanRoot,
	}, true
}
