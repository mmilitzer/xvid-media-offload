package lockfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockAcquireAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("unexpected error acquiring lock: %v", err)
	}
	defer lock.Release()

	// File should exist.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected lock file to exist: %v", err)
	}
}

func TestLockExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	lock1, err := Acquire(path)
	if err != nil {
		t.Fatalf("unexpected error acquiring first lock: %v", err)
	}
	defer lock1.Release()

	// Second acquire should fail.
	lock2, err := Acquire(path)
	if err == nil {
		lock2.Release()
		t.Fatal("expected second lock acquisition to fail")
	}
}

func TestLockReleasedAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	lock1, err := Acquire(path)
	if err != nil {
		t.Fatalf("unexpected error acquiring first lock: %v", err)
	}

	if err := lock1.Release(); err != nil {
		t.Fatalf("unexpected error releasing lock: %v", err)
	}

	// After release, re-acquire should succeed.
	lock2, err := Acquire(path)
	if err != nil {
		t.Fatalf("unexpected error re-acquiring lock: %v", err)
	}
	defer lock2.Release()
}
