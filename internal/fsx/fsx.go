// Package fsx holds the one rule lookout sorts filesystem errors by.
package fsx

import (
	"errors"
	"io/fs"
	"syscall"
)

// Absent reports whether err from inspecting a path means nothing is there,
// as opposed to lookout being unable to look. A path running through a file,
// which a PATH element or a pattern's literal element after a wildcard can
// produce, names nothing, as a missing one does. Every inspection lookout
// makes sorts its errors here, so the two can never be told apart
// differently in different places.
func Absent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}
