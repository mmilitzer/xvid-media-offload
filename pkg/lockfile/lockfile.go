package lockfile

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Lock represents an advisory file lock held by the daemon.
type Lock struct {
	fd   int
	path string
}

// Acquire opens or creates the lock file and acquires an exclusive,
// non-blocking flock. If another process holds the lock, it returns an error.
func Acquire(path string) (*Lock, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening lock file %s: %w", path, err)
	}

	err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("daemon already running (lock file %s is held)", path)
	}

	return &Lock{fd: fd, path: path}, nil
}

// Release closes the lock file descriptor, releasing the flock.
func (l *Lock) Release() error {
	if l.fd < 0 {
		return nil
	}
	err := unix.Close(l.fd)
	l.fd = -1
	return err
}
