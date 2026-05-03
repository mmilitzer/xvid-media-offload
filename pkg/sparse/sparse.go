package sparse

import (
	"fmt"

	"golang.org/x/sys/unix"
)

const safetyMargin = 0.8

// IsSparse reports whether the file at path appears to be sparse (its
// allocated blocks are meaningfully smaller than its logical size).
func IsSparse(path string) (bool, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return isSparseFromStat(&st), nil
}

func isSparseFromStat(st *unix.Stat_t) bool {
	allocatedBytes := int64(st.Blocks) * 512
	logicalSize := st.Size
	if logicalSize <= 0 {
		return false
	}
	return float64(allocatedBytes) < safetyMargin*float64(logicalSize)
}

// AllocatedBytes returns the number of bytes actually allocated on disk for
// the file at path.
func AllocatedBytes(path string) (int64, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	return int64(st.Blocks) * 512, nil
}
