// Command lookout reports how recently each of your workstreams moved and
// whether every input it relies on is healthy.
package main

import (
	"io"
	"log"
	"os"
	"time"
)

const (
	// exitFailure reports a command that was called correctly but could not do
	// its job, such as an invalid configuration file or a refused edit.
	exitFailure = 1
	// exitUsage is the conventional status for a command-line usage error, kept
	// distinct from exitFailure so a script can tell "you called it wrong" from
	// "it failed".
	exitUsage = 2
)

const usage = `usage:
  lookout read --json [--setting <name>]
  lookout unplaced [--setting <name>]
  lookout adopt [--setting <name>] [--new] <input>:<id> <workstream>
  lookout dismiss [--setting <name>] <input>:<id>
  lookout order [--setting <name>] <workstream>...
  lookout complete [--setting <name>] <workstream>`

// environment is what an invocation reads from the process beyond its
// arguments, passed in so specs can fix the clock and the variables.
type environment struct {
	getenv func(string) string
	now    func() time.Time
}

func main() {
	os.Exit(run(os.Args[1:], environment{getenv: os.Getenv, now: time.Now}, os.Stdout, os.Stderr))
}

// run dispatches one invocation. Diagnostics go to stderr so that stdout carries
// only command output, which callers are expected to parse.
func run(args []string, env environment, stdout, stderr io.Writer) int {
	diag := log.New(stderr, "", 0)
	if len(args) == 0 {
		diag.Print(usage)
		return exitUsage
	}
	commands := map[string]func([]string, environment, io.Writer, *log.Logger) int{
		"read":     read,
		"unplaced": unplaced,
		"adopt":    adopt,
		"dismiss":  dismiss,
		"order":    order,
		"complete": complete,
	}
	if cmd, ok := commands[args[0]]; ok {
		return cmd(args[1:], env, stdout, diag)
	}
	diag.Printf("lookout: unknown command %q\n%s", args[0], usage)
	return exitUsage
}
