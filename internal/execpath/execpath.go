// Package execpath finds an executable on the PATH an invocation was given.
package execpath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Find returns the first regular, executable file called name in a directory
// on getenv's PATH, or "" when there is none. exec.Command would search the
// PATH this process started with instead, which is not the environment the
// invocation was given. unsearched counts the directories that could not be
// looked into while nothing was found, so "not found" is not claimed of them.
func Find(getenv func(string) string, name string) (path string, unsearched int) {
	for _, dir := range filepath.SplitList(getenv("PATH")) {
		// A relative or empty element names the working directory, where an
		// executable nobody installed may sit; os/exec refuses those for the
		// same reason.
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return candidate, 0
		}
		// A missing directory, or a file listed on PATH in place of one,
		// holds nothing to find; anything else is a directory not searched.
		if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			unsearched++
		}
	}
	return "", unsearched
}
