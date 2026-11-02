//go:build !unix

package dirlock

import "os"

// Lock has no implementation off Unix.
func Lock(string, string, bool) (*os.File, error) {
	return nil, ErrUnsupported
}
