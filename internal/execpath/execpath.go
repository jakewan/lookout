// Package execpath finds an executable on the PATH an invocation was given.
package execpath

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jakewan/lookout/internal/fsx"
)

// Find returns the first regular, executable file called name in a directory
// on getenv's PATH. exec.Command would search the PATH this process started
// with instead, which is not the environment the invocation was given. When
// there is none, missing is a line for people saying so, which counts the
// directories that could not be looked into, so "not found" is not claimed of
// them.
func Find(getenv func(string) string, name string) (path, missing string) {
	unsearched := 0
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
			return candidate, ""
		}
		// A missing directory, or a file listed on PATH in place of one,
		// holds nothing to find; anything else is a directory not searched.
		if err != nil && !fsx.Absent(err) {
			unsearched++
		}
	}
	switch unsearched {
	case 0:
		return "", name + ": not found on PATH"
	case 1:
		return "", name + ": not found on PATH, and 1 directory on it could not be searched"
	}
	return "", fmt.Sprintf("%s: not found on PATH, and %d directories on it could not be searched", name, unsearched)
}
