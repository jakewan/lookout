//go:build unix

package dirlock

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// Lock takes the lock on dir, which errors name as what, such as "the state
// directory". A shared lock on a directory that does not exist yet takes
// nothing and returns nil. The lock is released when the returned file is
// closed, including by the process ending.
func Lock(dir, what string, exclusive bool) (*os.File, error) {
	f, err := os.Open(dir)
	if errors.Is(err, fs.ErrNotExist) && !exclusive {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", what, err)
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
		return nil, errors.Join(fmt.Errorf("locking %s: %w", what, err), f.Close())
	}
	return f, nil
}
