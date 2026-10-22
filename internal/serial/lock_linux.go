package serial

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

const CompatibilityLockPath = "/run/sc200_fan_control.lock"

// Lock owns an advisory lock. Never unlink a lock file: waiters may own its inode.
type Lock struct{ file *os.File }

func AcquireLock(path string) (*Lock, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0660)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s busy: %w", path, err)
	}
	return &Lock{file: f}, nil
}
func (l *Lock) Close() error { return l.file.Close() }

// AcquireCompatibilityLock is process-wide, held by the manager until all ports close.
func AcquireCompatibilityLock() (*Lock, error) { return AcquireLock(CompatibilityLockPath) }
