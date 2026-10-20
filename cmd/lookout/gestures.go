package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"slices"
	"strings"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/state"
)

// adopt adds an item to a workstream in the configuration file.
func adopt(args []string, env environment, _ io.Writer, diag *log.Logger) int {
	fs, setting := newFlags("adopt")
	create := fs.Bool("new", false, "")
	pos, ok := parseFlags(fs, setting, args, diag)
	if !ok {
		return exitUsage
	}
	if len(pos) != 2 {
		diag.Printf("lookout adopt: want an item and a workstream\n%s", usage)
		return exitUsage
	}
	m, ok := memberArg("adopt", pos[0], diag)
	if !ok {
		return exitUsage
	}
	g := state.Gesture{Gesture: "adopt", Item: pos[0], Workstream: pos[1], New: *create}
	return record(env, diag, *setting, g, func(path string, g *state.Gesture) error {
		return edit(path, func(d *config.Document) error {
			from, err := d.Adopt(m, pos[1], *create)
			g.From = from
			return err
		})
	})
}

// order moves workstreams to the front of the configuration's stated order.
func order(args []string, env environment, _ io.Writer, diag *log.Logger) int {
	fs, setting := newFlags("order")
	pos, ok := parseFlags(fs, setting, args, diag)
	if !ok {
		return exitUsage
	}
	if len(pos) == 0 {
		diag.Printf("lookout order: name at least one workstream\n%s", usage)
		return exitUsage
	}
	g := state.Gesture{Gesture: "order", Workstreams: pos}
	return record(env, diag, *setting, g, func(path string, _ *state.Gesture) error {
		return edit(path, func(d *config.Document) error { return d.Order(pos) })
	})
}

// complete marks a workstream finished as of today.
func complete(args []string, env environment, _ io.Writer, diag *log.Logger) int {
	fs, setting := newFlags("complete")
	pos, ok := parseFlags(fs, setting, args, diag)
	if !ok {
		return exitUsage
	}
	if len(pos) != 1 {
		diag.Printf("lookout complete: name one workstream\n%s", usage)
		return exitUsage
	}
	date := env.now().Format("2006-01-02")
	g := state.Gesture{Gesture: "complete", Workstream: pos[0]}
	return record(env, diag, *setting, g, func(path string, _ *state.Gesture) error {
		return edit(path, func(d *config.Document) error { return d.Complete(pos[0], date) })
	})
}

// dismiss hides an unplaced item until it is touched again. It writes only to
// state; the dismissal is the gesture itself.
func dismiss(args []string, env environment, _ io.Writer, diag *log.Logger) int {
	fs, setting := newFlags("dismiss")
	pos, ok := parseFlags(fs, setting, args, diag)
	if !ok {
		return exitUsage
	}
	if len(pos) != 1 {
		diag.Printf("lookout dismiss: name one item\n%s", usage)
		return exitUsage
	}
	m, ok := memberArg("dismiss", pos[0], diag)
	if !ok {
		return exitUsage
	}
	g := state.Gesture{Gesture: "dismiss", Item: pos[0]}
	return record(env, diag, *setting, g, func(path string, _ *state.Gesture) error {
		cfg, err := config.Load(path)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(cfg.Inputs, func(i config.Input) bool { return i.Name == m.Input }) {
			return config.Refuse("input %q is not in this configuration", m.Input)
		}
		// A completed workstream's member returns to unplaced once it moves, so
		// only a live workstream's claim rules a dismissal out.
		for _, w := range cfg.Workstreams {
			if w.Completed == "" && slices.Contains(w.Members, m) {
				return config.Refuse("%s belongs to workstream %q, so it is never unplaced", pos[0], w.Name)
			}
		}
		return nil
	})
}

func memberArg(cmd, arg string, diag *log.Logger) (config.Member, bool) {
	input, id, ok := strings.Cut(arg, ":")
	if !ok || input == "" || id == "" {
		diag.Printf("lookout %s: %q is not an item: write it as <input>:<id>, as lookout unplaced lists it\n%s", cmd, arg, usage)
		return config.Member{}, false
	}
	return config.Member{Input: input, ID: id}, true
}

func edit(path string, change func(*config.Document) error) error {
	doc, err := config.Open(path)
	if err != nil {
		return err
	}
	if err := change(doc); err != nil {
		return err
	}
	return doc.Save()
}

// record runs one gesture against the located setting and appends it to the
// gesture log whatever its outcome, since a refused attempt is upkeep too. A
// failure to find the setting records nothing: there is no log to write to.
func record(env environment, diag *log.Logger, setting string, g state.Gesture, act func(path string, g *state.Gesture) error) int {
	cmd := g.Gesture
	loc, err := locate(env, setting)
	if err != nil {
		diag.Printf("lookout %s: %v", cmd, err)
		return exitFailure
	}
	// Claiming the state before acting keeps a configuration that does not own
	// it from editing first and then finding it has nowhere to record.
	if claimErr := loc.store.Claim(); claimErr != nil {
		diag.Printf("lookout %s: %v", cmd, claimErr)
		return exitFailure
	}
	g.At = env.now()
	err = act(loc.path, &g)
	g.Outcome = state.OK
	if err != nil {
		g.Outcome, g.Reason = outcome(err), err.Error()
	}
	if appendErr := loc.store.Append(g); appendErr != nil {
		if g.Outcome == state.OK {
			// A dismissal is its gesture, so an unrecorded one never happened.
			if cmd == "dismiss" {
				diag.Printf("lookout dismiss: nothing was dismissed: %v", appendErr)
			} else {
				diag.Printf("lookout %s: the configuration was changed but the change was not recorded in the gesture log: %v", cmd, appendErr)
			}
			return exitFailure
		}
		err = errors.Join(err, fmt.Errorf("recording it in the gesture log: %w", appendErr))
	}
	if err != nil {
		diag.Printf("lookout %s: %v", cmd, err)
		return exitFailure
	}
	return 0
}

func outcome(err error) string {
	if _, ok := errors.AsType[*config.Refusal](err); ok {
		return state.Refused
	}
	if errors.Is(err, config.ErrNotDurable) {
		return state.OK
	}
	return state.Failed
}
