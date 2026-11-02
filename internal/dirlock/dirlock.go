// Package dirlock takes an advisory lock on a directory itself, so no lock
// file needs creating: exclusive for a writer, shared for a reader. Callers
// decide what to do where a system offers no such lock.
package dirlock

import "errors"

// ErrUnsupported is returned where no directory lock is to be had: off Unix,
// or on a filesystem that offers none.
var ErrUnsupported = errors.New("no directory lock is available here")
