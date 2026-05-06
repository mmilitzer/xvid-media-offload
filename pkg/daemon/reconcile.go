package daemon

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/mmilitzer/xvid-media-offload/pkg/db"
	"github.com/mmilitzer/xvid-media-offload/pkg/marker"
	"github.com/mmilitzer/xvid-media-offload/pkg/sparse"
)

// reconcileDB performs startup reconciliation between the database and the filesystem.
func (d *Daemon) reconcileDB() error {
	records, err := d.database.ListAllOffloadedFiles()
	if err != nil {
		return err
	}

	for _, rec := range records {
		if _, ok := findScanRootForPath(rec.LocalPath, d.cfg.ScanRoots); !ok {
			log.Printf("DB reconcile: skipping %s because it is outside active scan_roots", rec.LocalPath)
			continue
		}
		d.reconcileRecord(rec)
	}

	return nil
}

// findScanRootForPath returns the matching clean/absolute scan root for path,
// or false if none. It uses filepath.Rel instead of naive string prefix
// matching so that sibling paths such as /site/content2 are not treated as
// children of /site/content.
func findScanRootForPath(path string, roots []string) (string, bool) {
	cleanPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", false
	}

	for _, root := range roots {
		cleanRoot, err := filepath.Abs(filepath.Clean(root))
		if err != nil {
			continue
		}

		rel, err := filepath.Rel(cleanRoot, cleanPath)
		if err != nil {
			continue
		}

		if rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, "..") {
			return cleanRoot, true
		}
	}

	return "", false
}

func (d *Daemon) reconcileRecord(rec db.OffloadedRecord) {
	path := rec.LocalPath

	// Case 1: MP4 file no longer exists.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if d.dryRun {
			log.Printf("dry-run DB reconcile: would remove DB row for missing file %s", path)
			return
		}
		if err := d.database.DeleteOffloadedFile(path); err != nil {
			log.Printf("DB reconcile: failed to delete row for missing file %s: %v", path, err)
		} else {
			log.Printf("DB reconcile: removed row for missing file %s", path)
		}
		return
	}

	isSparse, err := sparse.IsSparse(path)
	if err != nil {
		log.Printf("DB reconcile: sparse check failed for %s: %v", path, err)
		return
	}

	// Case 2: MP4 exists and is no longer sparse, size matches original.
	if !isSparse {
		fi, err := os.Stat(path)
		if err == nil && fi.Size() == rec.OriginalSize {
			if d.dryRun {
				log.Printf("dry-run DB reconcile: would remove DB row for restored file %s", path)
				return
			}
			if err := d.database.DeleteOffloadedFile(path); err != nil {
				log.Printf("DB reconcile: failed to delete row for restored file %s: %v", path, err)
			} else {
				log.Printf("DB reconcile: removed row for restored file %s", path)
			}
		}
		return
	}

	scanRoot, ok := findScanRootForPath(path, d.cfg.ScanRoots)
	if !ok {
		// This should not happen because reconcileDB already filters, but be safe.
		log.Printf("DB reconcile: skipping %s because it is outside active scan_roots", path)
		return
	}

	// File is sparse. Walk upward to find the main marker file first (bounded by scanRoot).
	markerPath, markerDir := findMarkerForPathWithin(path, d.cfg.MarkerFilename, scanRoot)
	mInfo, err := marker.Parse(markerPath)
	markerValid := err == nil && mInfo.Valid
	markerHasFileID := false
	if markerValid && markerDir != "" {
		relPath, _ := filepath.Rel(markerDir, path)
		relPath = filepath.ToSlash(relPath)
		_, markerHasFileID = mInfo.MatchFileID(relPath)
	}

	offloadedMarkerPath := path + ".offloaded"
	_, offloadedMarkerErr := os.Stat(offloadedMarkerPath)
	hasOffloadedMarker := offloadedMarkerErr == nil

	if markerValid && markerHasFileID {
		// Case 3: marker exists with file id.
		if hasOffloadedMarker {
			// Still intentionally offloaded. Register an inotify watch.
			_ = d.addInotifyWatch(filepath.Dir(path))
		}
		// If .offloaded is missing, normal scan will handle it.
		return
	}

	// Case 4 & 5: marker is missing, invalid, or no longer contains the file's
	// remote file id. The set is no longer managed by our service.
	if hasOffloadedMarker {
		if d.dryRun {
			log.Printf("dry-run DB reconcile: would remove .offloaded marker for %s", path)
		} else {
			if err := os.Remove(offloadedMarkerPath); err != nil {
				log.Printf("DB reconcile: failed to remove .offloaded marker for %s: %v", path, err)
			} else {
				log.Printf("DB reconcile: removed .offloaded marker for %s", path)
			}
		}
	}

	if d.dryRun {
		log.Printf("dry-run DB reconcile: would enqueue DB-only restore for %s", path)
		return
	}

	job := restoreJob{
		Path:       path,
		RemoteID:   rec.RemoteFileID,
		Autograph:  rec.Autograph,
		Size:       rec.OriginalSize,
		MarkerPath: markerPath,
		ScanRoot:   scanRoot,
	}
	d.enqueueRestoreBlocking(d.ctx, job)
	log.Printf("DB reconcile: enqueued DB-only restore for %s", path)
}
