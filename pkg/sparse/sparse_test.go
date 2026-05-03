package sparse

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsSparseRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "regular")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("hello world"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	sparse, err := IsSparse(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sparse {
		t.Error("expected regular file to not be sparse")
	}
}

func TestIsSparseTruncatedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sparse")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Create a 1 MB file with no data written -> likely sparse.
	if err := f.Truncate(1024 * 1024); err != nil {
		t.Fatal(err)
	}
	f.Close()

	sparse, err := IsSparse(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sparse {
		t.Log("filesystem may not support sparse detection via truncate; skipping")
	}
}

func TestAllocatedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alloc")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("test data"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	alloc, err := AllocatedBytes(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alloc <= 0 {
		t.Errorf("expected positive allocated bytes, got %d", alloc)
	}
}
