package punch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mmilitzer/xvid-media-offload/pkg/sparse"
	"golang.org/x/sys/unix"
)

func TestPunchHoleIntegration(t *testing.T) {
	if os.Getenv("RUN_HOLE_PUNCH_TESTS") != "1" {
		t.Skip("Set RUN_HOLE_PUNCH_TESTS=1 to run hole punch integration tests")
	}

	path := filepath.Join(t.TempDir(), "punch-test")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	// Write 2 MB of non-zero data.
	data := make([]byte, 2*1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	f.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	originalSize := fi.Size()

	err = PunchHole(path, 64*1024) // keep 64 KB prefix
	if err != nil {
		if err == unix.EOPNOTSUPP {
			t.Skip("filesystem does not support hole punching")
		}
		t.Fatalf("punch hole failed: %v", err)
	}

	// Verify logical size unchanged.
	fi, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != originalSize {
		t.Errorf("logical size changed: got %d, want %d", fi.Size(), originalSize)
	}

	// Verify file is now sparse.
	isSparse, err := sparse.IsSparse(path)
	if err != nil {
		t.Fatalf("sparse check failed: %v", err)
	}
	if !isSparse {
		t.Error("expected file to be sparse after punching")
	}
}

func TestPunchHoleTooSmall(t *testing.T) {
	if os.Getenv("RUN_HOLE_PUNCH_TESTS") != "1" {
		t.Skip("Set RUN_HOLE_PUNCH_TESTS=1 to run hole punch integration tests")
	}

	path := filepath.Join(t.TempDir(), "small")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("tiny"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	err = PunchHole(path, 1024)
	if err == nil {
		t.Error("expected error for file too small to punch")
	}
}
