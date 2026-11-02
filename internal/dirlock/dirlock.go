// Package dirlock takes an advisory lock on a directory itself, so no lock
// file needs creating: exclusive for a writer, shared for a reader. Callers
// decide what to do where a system offers no such lock.
package dirlock

import "errors"

// ErrUnsupported is returned where the system offers no directory lock.
var ErrUnsupported = errors.New("directory locks are available only on Unix systems")
