package db

import (
	"path/filepath"
	"testing"
	"time"
)

func TestOpenCreatesTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer db.Close()
}

func TestUpsertOffloadedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer db.Close()

	err = db.UpsertOffloadedFile("/media/video.mp4", "abc123", 1, 1024000, time.Now(), 1000, 1000, 0644)
	if err != nil {
		t.Fatalf("unexpected error on upsert: %v", err)
	}

	// Upsert again with different values (idempotent).
	err = db.UpsertOffloadedFile("/media/video.mp4", "abc456", 0, 2048000, time.Now(), 1001, 1001, 0640)
	if err != nil {
		t.Fatalf("unexpected error on second upsert: %v", err)
	}
}

func TestUpsertInvalidAutograph(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("unexpected error opening db: %v", err)
	}
	defer db.Close()

	// autograph must be 0 or 1 per the CHECK constraint.
	err = db.UpsertOffloadedFile("/media/video.mp4", "abc123", 2, 1024000, time.Now(), 0, 0, 0)
	if err == nil {
		t.Error("expected error for invalid autograph value")
	}
}
