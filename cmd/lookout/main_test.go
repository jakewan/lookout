package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunWithoutCommandPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), nil, environment{}, &stdout, &stderr)

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Errorf("stderr = %q, want a usage line", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing: it is reserved for command output", stdout.String())
	}
}

// TestRunRejectsUnknownCommand pins the operator-mistake case: a mistyped
// command must fail loudly and name what it did not recognize, rather than fall
// through to some default behavior the operator did not ask for.
func TestRunRejectsUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"raed"}, environment{}, &stdout, &stderr)

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), `unknown command "raed"`) {
		t.Errorf("stderr = %q, want it to name the unknown command", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}
