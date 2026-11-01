package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"slices"
	"strconv"
	"strings"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/state"
)

// adopt adds an item to a workstream in the configuration file.
func adopt(_ context.Context, args []string, env environment, _ io.Writer, diag *log.Logger) int {
	return place("adopt", args, env, diag, (*config.Document).Adopt)
}

// place runs adopt or move: each puts an item in a workstream, created at the
// end of the order with --new, which may then be given a title.
func place(name string, args []string, env environment, diag *log.Logger, put func(*config.Document, config.Member, string, bool) ([]string, error)) int {
	fs, setting := newFlags(name)
	create := fs.Bool("new", false, "")
	title := fs.String("title", "", "")
	pos, ok := parseFlags(fs, setting, args, diag)
	if !ok {
		return exitUsage
	}
	if len(pos) != 2 {
		diag.Printf("lookout %s: want an item and a workstream\n%s", name, usage)
		return exitUsage
	}
	titled := false
	fs.Visit(func(f *flag.Flag) { titled = titled || f.Name == "title" })
	if titled && !*create {
		diag.Printf("lookout %s: --title applies only with --new: to title a workstream that exists, use lookout rename\n%s", name, usage)
		return exitUsage
	}
	m, ok := memberArg(name, pos[0], diag)
	if !ok {
		return exitUsage
	}
	g := state.Gesture{Gesture: name, Item: m.Input + ":" + m.ID, Workstream: pos[1], New: *create, To: *title}
	return record(env, diag, fs.Name(), *setting, g, func(path string, g *state.Gesture) error {
		return edit(path, func(d *config.Document) error {
			from, err := put(d, m, pos[1], *create)
			g.From = from
			if err != nil || !titled {
				return err
			}
			return d.SetTitle(pos[1], *title)
		})
	})
}

// order moves workstreams to the front of the configuration's stated order.
func order(_ context.Context, args []string, env environment, _ io.Writer, diag *log.Logger) int {
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
	return record(env, diag, fs.Name(), *setting, g, func(path string, _ *state.Gesture) error {
		return edit(path, func(d *config.Document) error { return d.Order(pos) })
	})
}

// complete marks a workstream finished as of today.
func complete(_ context.Context, args []string, env environment, _ io.Writer, diag *log.Logger) int {
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
	g := state.Gesture{Gesture: "complete", Workstream: pos[0], Date: date}
	return record(env, diag, fs.Name(), *setting, g, func(path string, _ *state.Gesture) error {
		return edit(path, func(d *config.Document) error { return d.Complete(pos[0], date) })
	})
}

// move takes an item out of its workstream and into another.
func move(_ context.Context, args []string, env environment, _ io.Writer, diag *log.Logger) int {
	return place("move", args, env, diag, (*config.Document).Move)
}

// remove takes an item out of every workstream that lists it.
func remove(_ context.Context, args []string, env environment, _ io.Writer, diag *log.Logger) int {
	fs, setting := newFlags("remove")
	pos, ok := parseFlags(fs, setting, args, diag)
	if !ok {
		return exitUsage
	}
	if len(pos) != 1 {
		diag.Printf("lookout remove: name one item\n%s", usage)
		return exitUsage
	}
	m, ok := memberArg("remove", pos[0], diag)
	if !ok {
		return exitUsage
	}
	g := state.Gesture{Gesture: "remove", Item: m.Input + ":" + m.ID}
	return record(env, diag, fs.Name(), *setting, g, func(path string, g *state.Gesture) error {
		return edit(path, func(d *config.Document) error {
			from, err := d.Remove(m)
			g.From = from
			return err
		})
	})
}

// rename gives a workstream a new title, keeping its id; with --clear it
// removes the title, and with --id it changes the id instead.
func rename(_ context.Context, args []string, env environment, _ io.Writer, diag *log.Logger) int {
	fs, setting := newFlags("rename")
	id := fs.Bool("id", false, "")
	clearTitle := fs.Bool("clear", false, "")
	pos, ok := parseFlags(fs, setting, args, diag)
	if !ok {
		return exitUsage
	}
	switch {
	case *id && *clearTitle:
		diag.Printf("lookout rename: --id and --clear cannot be combined\n%s", usage)
		return exitUsage
	case *clearTitle && len(pos) != 1:
		diag.Printf("lookout rename: --clear wants one workstream\n%s", usage)
		return exitUsage
	case *id && len(pos) != 2:
		diag.Printf("lookout rename: want a workstream and its new id\n%s", usage)
		return exitUsage
	case !*id && !*clearTitle && len(pos) != 2:
		diag.Printf("lookout rename: want a workstream and its new title\n%s", usage)
		return exitUsage
	}
	if *id {
		g := state.Gesture{Gesture: "rename", Workstream: pos[0], To: pos[1]}
		return record(env, diag, fs.Name(), *setting, g, func(path string, _ *state.Gesture) error {
			return edit(path, func(d *config.Document) error { return d.Rename(pos[0], pos[1]) })
		})
	}
	// Title changes are logged apart from id changes, so the baseline, which
	// follows a workstream across logged renames, never takes a title for an id.
	g := state.Gesture{Gesture: "retitle", Workstream: pos[0]}
	if *clearTitle {
		return record(env, diag, fs.Name(), *setting, g, func(path string, _ *state.Gesture) error {
			return edit(path, func(d *config.Document) error { return d.ClearTitle(pos[0]) })
		})
	}
	g.To = pos[1]
	code := record(env, diag, fs.Name(), *setting, g, func(path string, _ *state.Gesture) error {
		return edit(path, func(d *config.Document) error { return d.SetTitle(pos[0], pos[1]) })
	})
	if code == 0 {
		diag.Printf("lookout rename: set the title of %s; its id is unchanged (to change the id, use lookout rename --id)", pos[0])
	}
	return code
}

// dismiss hides an unplaced item until it is touched again. It writes only to
// state; the dismissal is the gesture itself.
func dismiss(_ context.Context, args []string, env environment, _ io.Writer, diag *log.Logger) int {
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
	g := state.Gesture{Gesture: "dismiss", Item: m.Input + ":" + m.ID}
	return record(env, diag, fs.Name(), *setting, g, func(path string, _ *state.Gesture) error {
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
				return config.Refuse("%s belongs to workstream %q, so it is never unplaced", g.Item, w.Name)
			}
		}
		return nil
	})
}

// memberArg reads an <input>:<id> argument. A token lookout unplaced printed
// quoted, because its id holds a tab or line break, is decoded; an input name
// never starts with a quote, so a quoted token cannot be read as another.
func memberArg(cmd, arg string, diag *log.Logger) (config.Member, bool) {
	if strings.HasPrefix(arg, `"`) {
		decoded, err := strconv.Unquote(arg)
		if err != nil {
			diag.Printf("lookout %s: %s is quoted but not as lookout unplaced quotes it: %v\n%s", cmd, arg, err, usage)
			return config.Member{}, false
		}
		arg = decoded
	}
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
func record(env environment, diag *log.Logger, cmd, setting string, g state.Gesture, act func(path string, g *state.Gesture) error) int {
	loc, err := locate(env, setting, state.Write)
	if err != nil {
		diag.Printf("lookout %s: %v", cmd, err)
		return exitFailure
	}
	// The state stays held until the gesture is recorded, so another lookout
	// invocation for this setting cannot edit or record in between.
	defer func() {
		if closeErr := loc.store.Close(); closeErr != nil {
			diag.Printf("lookout %s: releasing the state: %v", cmd, closeErr)
		}
	}()
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
