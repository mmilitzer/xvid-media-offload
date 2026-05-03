package punch

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// PunchHole punches a hole in the file at path starting at keepPrefixBytes
// and extending to the end of the file. The logical file size is preserved.
func PunchHole(path string, keepPrefixBytes int64) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}

	size := fi.Size()
	if size <= 2*keepPrefixBytes {
		return fmt.Errorf("file size %d <= 2*keep_prefix_bytes (%d), nothing useful to offload", size, 2*keepPrefixBytes)
	}

	offset := keepPrefixBytes
	length := size - keepPrefixBytes

	err = unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, offset, length)
	if err != nil {
		return fmt.Errorf("fallocate punch hole: %w", err)
	}

	return nil
}
