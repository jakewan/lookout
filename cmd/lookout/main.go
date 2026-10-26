// Command lookout reports how recently each of your workstreams moved and
// whether every input it relies on is healthy.
package main

import (
	"context"
	"io"
	"log"
	"os"
	"os/signal"
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
  lookout show [--setting <name>]
  lookout unplaced [--setting <name>]
  lookout adopt [--setting <name>] [--new] <input>:<id> <workstream>
  lookout dismiss [--setting <name>] <input>:<id>
  lookout order [--setting <name>] <workstream>...
  lookout complete [--setting <name>] <workstream>
  lookout move [--setting <name>] [--new] <input>:<id> <workstream>
  lookout remove [--setting <name>] <input>:<id>
  lookout rename [--setting <name>] <workstream> <new-name>
  lookout snapshot [--setting <name>]`

// environment is what an invocation reads from the process beyond its
// arguments, passed in so specs can fix the clock and the variables.
type environment struct {
	getenv func(string) string
	now    func() time.Time
	// github is the GraphQL endpoint GitHub inputs are read from. It is not
	// read from the environment or the configuration, where one line could
	// send the github.com token to another host.
	github string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	// Restoring the default handling on the first interrupt lets a second one
	// end the process even where lookout is waiting on something it cannot
	// cancel, such as another invocation's hold on the state.
	context.AfterFunc(ctx, stop)
	code := run(ctx, os.Args[1:], environment{getenv: os.Getenv, now: time.Now, github: "https://api.github.com/graphql"}, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run dispatches one invocation. Diagnostics go to stderr so that stdout carries
// only command output, which callers are expected to parse. Cancelling ctx
// stops the reading of inputs.
func run(ctx context.Context, args []string, env environment, stdout, stderr io.Writer) int {
	diag := log.New(stderr, "", 0)
	if len(args) == 0 {
		diag.Print(usage)
		return exitUsage
	}
	commands := map[string]func(context.Context, []string, environment, io.Writer, *log.Logger) int{
		"read":     read,
		"show":     show,
		"unplaced": unplaced,
		"adopt":    adopt,
		"dismiss":  dismiss,
		"order":    order,
		"complete": complete,
		"move":     move,
		"remove":   remove,
		"rename":   rename,
		"snapshot": snapshot,
	}
	if cmd, ok := commands[args[0]]; ok {
		return cmd(ctx, args[1:], env, stdout, diag)
	}
	diag.Printf("lookout: unknown command %q\n%s", args[0], usage)
	return exitUsage
}
