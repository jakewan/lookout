package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lockfile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "package-lock.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestALockfileFromThePublicRegistryPasses(t *testing.T) {
	path := lockfile(t, `{"lockfileVersion": 3, "packages": {
		"": {"name": "tools"},
		"node_modules/a": {"version": "1.0.0", "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz", "integrity": "sha512-x"}
	}}`)
	var stderr bytes.Buffer
	if code := run([]string{path}, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestAPackageFromAnywhereElseIsRefusedByName(t *testing.T) {
	cases := map[string]string{
		"another registry": `"resolved": "https://npm.example.com/b/-/b-1.0.0.tgz", "integrity": "sha512-x"`,
		"a lookalike host": `"resolved": "https://registry.npmjs.org.example.com/b/-/b-1.0.0.tgz", "integrity": "sha512-x"`,
		"plain http":       `"resolved": "http://registry.npmjs.org/b/-/b-1.0.0.tgz", "integrity": "sha512-x"`,
		"a git source":     `"resolved": "git+ssh://git@example.com/b.git#abc", "integrity": "sha512-x"`,
		"no source":        `"integrity": "sha512-x"`,
		"no integrity":     `"resolved": "https://registry.npmjs.org/b/-/b-1.0.0.tgz"`,
		"a local link":     `"resolved": "../b", "link": true`,
	}
	for name, fields := range cases {
		t.Run(name, func(t *testing.T) {
			path := lockfile(t, `{"lockfileVersion": 3, "packages": {
				"": {"name": "tools"},
				"node_modules/a": {"version": "1.0.0", "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz", "integrity": "sha512-x"},
				"node_modules/b": {"version": "1.0.0", `+fields+`}
			}}`)
			var stderr bytes.Buffer
			if code := run([]string{path}, &stderr); code == 0 {
				t.Fatal("a package from outside the public registry passed")
			}
			if !strings.Contains(stderr.String(), "node_modules/b") || strings.Contains(stderr.String(), "node_modules/a") {
				t.Errorf("stderr should name only node_modules/b: %q", stderr.String())
			}
		})
	}
}

func TestAnUnreadableLockfileFails(t *testing.T) {
	for name, path := range map[string]string{
		"missing":     filepath.Join(t.TempDir(), "package-lock.json"),
		"not JSON":    lockfile(t, `not json`),
		"no packages": lockfile(t, `{"lockfileVersion": 3}`),
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := run([]string{path}, &stderr); code == 0 {
				t.Fatal("an unreadable lockfile passed")
			}
		})
	}
}
