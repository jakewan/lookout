//go:build unix

package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// lockDir takes an advisory lock on the state directory itself, so no lock
// file needs creating: exclusive for a gesture, shared for a read. A read of a
// directory that does not exist yet takes nothing and returns nil. The lock is
// released when the returned file is closed, including by the process ending.
func lockDir(dir string, exclusive bool) (*os.File, error) {
	f, err := os.Open(dir)
	if errors.Is(err, fs.ErrNotExist) && !exclusive {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening the state directory: %w", err)
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	for {
		err = syscall.Flock(int(f.Fd()), how)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("locking the state directory: %w", err), f.Close())
	}
	return f, nil
}
