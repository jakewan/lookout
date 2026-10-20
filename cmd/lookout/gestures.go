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
	return record(env, diag, *setting, g, func(path string) error {
		return edit(path, func(d *config.Document) error { return d.Adopt(m, pos[1], *create) })
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
	return record(env, diag, *setting, g, func(path string) error {
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
	return record(env, diag, *setting, g, func(path string) error {
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
	return record(env, diag, *setting, g, func(path string) error {
		cfg, err := config.Load(path)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(cfg.Inputs, func(i config.Input) bool { return i.Name == m.Input }) {
			return refusal{fmt.Sprintf("input %q is not in this configuration", m.Input)}
		}
		for _, w := range cfg.Workstreams {
			if slices.Contains(w.Members, m) {
				return refusal{fmt.Sprintf("%s belongs to workstream %q, so it is never unplaced", pos[0], w.Name)}
			}
		}
		return nil
	})
}

// refusal is a gesture lookout declines for a reason the user can act on.
type refusal struct{ msg string }

func (r refusal) Error() string { return r.msg }

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
func record(env environment, diag *log.Logger, setting string, g state.Gesture, act func(path string) error) int {
	cmd := g.Gesture
	loc, err := locate(env, setting)
	if err != nil {
		diag.Printf("lookout %s: %v", cmd, err)
		return exitFailure
	}
	g.At = env.now()
	err = act(loc.path)
	g.Outcome = state.OK
	if err != nil {
		g.Outcome, g.Reason = outcome(err), err.Error()
	}
	if appendErr := loc.store.Append(g); appendErr != nil {
		if err == nil {
			diag.Printf("lookout %s: the change was made but not recorded in the gesture log: %v", cmd, appendErr)
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
	if _, ok := errors.AsType[refusal](err); ok {
		return state.Refused
	}
	return state.Failed
}
