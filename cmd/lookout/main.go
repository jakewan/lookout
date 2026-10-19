// Command lookout is the entry point for lookout, which is to report how
// recently each of your workstreams moved and whether every input it relies on
// is healthy. It has no subcommands yet, so every invocation is a usage error.
package main

import (
	"io"
	"log"
	"os"
)

// exitUsage is the conventional status for a command-line usage error, kept
// distinct from 1 so a script can tell "you called it wrong" from "it failed".
const exitUsage = 2

const usage = "usage: lookout <command> [arguments]"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches one invocation. Diagnostics go to stderr so that stdout carries
// only command output, which callers are expected to parse.
func run(args []string, _, stderr io.Writer) int {
	diag := log.New(stderr, "", 0)
	if len(args) == 0 {
		diag.Print(usage)
		return exitUsage
	}
	diag.Printf("lookout: unknown command %q\n%s", args[0], usage)
	return exitUsage
}
