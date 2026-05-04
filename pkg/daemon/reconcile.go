package daemon

import (
	"log"
	"os"
	"path/filepath"

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
		d.reconcileRecord(rec)
	}

	return nil
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

	// File is sparse.
	offloadedMarkerPath := path + ".offloaded"
	_, offloadedMarkerErr := os.Stat(offloadedMarkerPath)
	hasOffloadedMarker := offloadedMarkerErr == nil

	if hasOffloadedMarker {
		// Case 3: still intentionally offloaded.
		// Register an inotify watch on the parent directory so we are notified
		// when the .offloaded marker is deleted, even if the main marker file
		// was removed and the normal scan no longer finds this folder.
		_ = d.addInotifyWatch(filepath.Dir(path))
		return
	}

	// .offloaded marker is missing. Walk upward to find the main marker file.
	markerPath, markerDir := findMarkerForPath(path, d.cfg.MarkerFilename)
	mInfo, err := marker.Parse(markerPath)
	markerValid := err == nil && mInfo.Valid
	markerHasFileID := false
	if markerValid && markerDir != "" {
		relPath, _ := filepath.Rel(markerDir, path)
		relPath = filepath.ToSlash(relPath)
		_, markerHasFileID = mInfo.MatchFileID(relPath)
	}

	if markerValid && markerHasFileID {
		// Case 4: marker exists with file id. Normal scan will handle it.
		return
	}

	// Case 5: DB-only recovery.
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
	}
	d.enqueueRestoreBlocking(d.ctx, job)
	log.Printf("DB reconcile: enqueued DB-only restore for %s", path)
}
