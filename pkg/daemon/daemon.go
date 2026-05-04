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
	"github.com/mmilitzer/xvid-media-offload/pkg/marker"
	"github.com/mmilitzer/xvid-media-offload/pkg/restore"
	"github.com/mmilitzer/xvid-media-offload/pkg/scanner"
	"github.com/mmilitzer/xvid-media-offload/pkg/shrink"
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

	ticker *time.Ticker

	inotifyFd int
	watches   map[string]int // dirPath -> watch descriptor
	watchesMu sync.Mutex

	queue      chan restoreJob
	inFlight   map[string]bool
	inFlightMu sync.Mutex
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
		cfg:        cfg,
		dryRun:     dryRun,
		database:   database,
		downloader: downloader,
		watches:    make(map[string]int),
		queue:      make(chan restoreJob, 100),
		inFlight:   make(map[string]bool),
	}
}

// Start begins the daemon and blocks until shutdown.
func (d *Daemon) Start() error {
	d.ctx, d.cancel = context.WithCancel(context.Background())
	defer d.cancel()

	// Signal handling.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	log.Println("daemon starting")

	// DB reconciliation.
	if d.database != nil {
		log.Println("startup DB reconciliation beginning")
		if err := d.reconcileDB(); err != nil {
			log.Printf("DB reconciliation error: %v", err)
		}
		log.Println("startup DB reconciliation complete")
	}

	// Start restore workers.
	for i := 0; i < d.cfg.RestoreWorkers; i++ {
		d.wg.Add(1)
		go d.restoreWorker()
	}

	// Start inotify reader if supported.
	if err := d.startInotify(); err != nil {
		log.Printf("inotify not available: %v", err)
	} else {
		d.wg.Add(1)
		go d.inotifyReader()
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
	d.cancel()

	if d.ticker != nil {
		d.ticker.Stop()
	}

	d.stopInotify()

	close(d.queue)
	d.wg.Wait()

	if d.database != nil {
		d.database.Close()
	}

	log.Println("daemon stopped")
}

func (d *Daemon) runScan() {
	log.Println("scan beginning")
	defer log.Println("scan complete")

	d.combinedScan()
}

// enqueueRestore adds a restore job if not already in flight.
func (d *Daemon) enqueueRestore(job restoreJob) bool {
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
	default:
		d.inFlightMu.Lock()
		delete(d.inFlight, job.Path)
		d.inFlightMu.Unlock()
		log.Printf("restore queue full, skipping: %s", job.Path)
		return false
	}
}

func (d *Daemon) restoreWorker() {
	defer d.wg.Done()
	for job := range d.queue {
		if d.ctx.Err() != nil {
			// Shutting down; clear in-flight and stop.
			d.inFlightMu.Lock()
			delete(d.inFlight, job.Path)
			d.inFlightMu.Unlock()
			continue
		}

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

	_, err = restore.RestoreOne(rjob, d.cfg, d.database, d.downloader)
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
	} else if !d.dryRun && len(shrinkRes.Offloaded) > 0 {
		log.Printf("shrink offloaded %d files", len(shrinkRes.Offloaded))
	} else if d.dryRun && len(shrinkRes.Candidates) > 0 {
		log.Printf("dry-run: would shrink %d files", len(shrinkRes.Candidates))
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

// resolveRestoreJob attempts to build a restoreJob for a file path by reading
// the marker file in its ancestor directories. It falls back to the DB if needed.
func (d *Daemon) resolveRestoreJob(path string) (restoreJob, bool) {
	dir := filepath.Dir(path)

	// Walk upward looking for the marker file.
	var markerPath string
	searchDir := dir
	for {
		candidate := filepath.Join(searchDir, d.cfg.MarkerFilename)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			markerPath = candidate
			break
		}
		parent := filepath.Dir(searchDir)
		if parent == searchDir {
			break
		}
		searchDir = parent
	}

	var remoteID string
	var autograph int = -1
	var size int64

	if markerPath != "" {
		if info, err := marker.Parse(markerPath); err == nil && info.Valid {
			relPath, _ := filepath.Rel(filepath.Dir(markerPath), path)
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
		if dbRemoteID, dbAutograph, dbSize, _, err := d.database.GetOffloadedFile(path); err == nil {
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

	// Determine scan root.
	scanRoot := ""
	for _, root := range d.cfg.ScanRoots {
		root = filepath.Clean(root)
		if strings.HasPrefix(path, root+string(filepath.Separator)) {
			scanRoot = root
			break
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
