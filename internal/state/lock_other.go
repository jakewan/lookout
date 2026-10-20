//go:build !unix

package state

import (
	"errors"
	"os"
)

// lockDir has no implementation off Unix, so state cannot be read or written
// there rather than being used without the lock that keeps it consistent.
func lockDir(string, bool) (*os.File, error) {
	return nil, errors.New("lookout keeps state only on Unix systems, where it can lock it")
}
