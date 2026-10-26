package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/jakewan/lookout/internal/claudecode"
	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/git"
	"github.com/jakewan/lookout/internal/github"
	"github.com/jakewan/lookout/internal/readout"
	"github.com/jakewan/lookout/internal/state"
	"github.com/jakewan/lookout/internal/supplied"
)

// located is the setting an invocation acts on.
type located struct {
	path  string
	store *state.Store
}

// locate finds the setting and holds its state as mode says; the caller closes
// the store.
func locate(env environment, setting string, mode state.Mode) (*located, error) {
	path, err := config.Locate(env.getenv, setting)
	if err != nil {
		return nil, err
	}
	name, err := config.Name(path)
	if err != nil {
		return nil, err
	}
	store, err := state.Open(env.getenv, name, path, mode)
	if err != nil {
		return nil, err
	}
	return &located{path: path, store: store}, nil
}

func currentReadout(ctx context.Context, env environment, setting string) (readout.Readout, error) {
	// A second attempt finds the state and holds its lock, so a third is
	// reached only if the state keeps vanishing and reappearing.
	for range 3 {
		out, again, err := readOnce(ctx, env, setting)
		if err != nil || !again {
			return out, err
		}
	}
	return readout.Readout{}, errors.New("the state directory kept appearing while lookout read it: run the command again")
}

// readOnce builds the readout, or reports that it must be built again.
func readOnce(ctx context.Context, env environment, setting string) (readout.Readout, bool, error) {
	r, again, err := readSetting(ctx, env, setting)
	return r.out, again, err
}

// reading is one readout together with what it was built from.
type reading struct {
	cfg       *config.Config
	dismissed map[string]time.Time
	now       time.Time
	out       readout.Readout
	// hub is the GitHub reader the inputs were read through, holding the
	// token it asked gh for, so a later GitHub read sends the same one.
	hub *github.Reader
}

// readSetting builds the readout, or reports that it must be built again. A
// read of a setting with no state yet holds no lock, so where state appeared
// before the configuration was loaded, a gesture may have changed both, and the
// read starts over holding the lock. A gesture creates the state before it
// changes anything, so a read that finds none afterward saw no gesture at all.
func readSetting(ctx context.Context, env environment, setting string) (reading, bool, error) {
	cfg, dismissed, again, err := settingNow(env, setting)
	if err != nil || again {
		return reading{}, again, err
	}
	now := env.now()
	hub := github.NewReader(env.getenv, env.github)
	readings, err := readInputs(ctx, env, cfg, now, hub)
	if err != nil {
		return reading{}, false, err
	}
	return reading{cfg: cfg, dismissed: dismissed, now: now, out: readout.Build(cfg, readings, dismissed, now), hub: hub}, false, nil
}

// settingNow loads the configuration and the dismissals together under the
// state's lock, then lets it go: inputs are not state, and reading them can
// take long enough that holding the lock would keep every gesture waiting.
func settingNow(env environment, setting string) (_ *config.Config, _ map[string]time.Time, again bool, err error) {
	loc, err := locate(env, setting, state.Read)
	if err != nil {
		return nil, nil, false, err
	}
	defer func() { err = errors.Join(err, loc.store.Close()) }()
	afterStateOpen()
	cfg, err := config.Load(loc.path)
	if err != nil {
		return nil, nil, false, err
	}
	if loc.store.Appeared() {
		return nil, nil, true, nil
	}
	return cfg, loc.store.Dismissals(), false, nil
}

// readInputs reads every input the configuration expects. A run interrupted
// part way fails, since the inputs it cut short would read as broken.
func readInputs(ctx context.Context, env environment, cfg *config.Config, now time.Time, hub *github.Reader) (map[string]readout.Reading, error) {
	files, err := supplied.NewReader()
	if err != nil {
		return nil, err
	}
	repos := git.NewReader(env.getenv)
	sessions := claudecode.NewReader(env.getenv)
	readings := make(map[string]readout.Reading, len(cfg.Inputs))
	keys := cfg.Keys()
	for _, in := range cfg.Inputs {
		scope := readout.Scope{Owners: cfg.ForgeOwners, Since: now.Add(-cfg.DiscoveryWindow), Members: cfg.MemberIDs(in.Name), Keys: keys}
		switch in.Kind {
		case config.KindSupplied:
			readings[in.Name] = files.Read(ctx, in, now)
		case config.KindGit:
			readings[in.Name] = repos.Read(ctx, in, scope, now)
		case config.KindGitHub:
			readings[in.Name] = hub.Read(ctx, in, scope, now)
		case config.KindClaudeCode:
			readings[in.Name] = sessions.Read(ctx, in, scope, now)
		default:
			// The configuration refuses a kind lookout has no reader for, so
			// this guards only a kind added there without one here.
			return nil, fmt.Errorf("input %q: lookout has no reader for kind %q", in.Name, in.Kind)
		}
	}
	if ctx.Err() != nil {
		return nil, errors.New("reading inputs: interrupted")
	}
	return readings, nil
}

// afterStateOpen runs once a read holds its state; specs use it to run
// gestures in between.
var afterStateOpen = func() {}

// newFlags is a command's flag set with the --setting flag every command takes.
func newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs, fs.String("setting", "", "")
}

// parseFlags parses args, letting flags follow the arguments as well as precede
// them, and returns the arguments. It reports a usage error itself.
func parseFlags(fs *flag.FlagSet, setting *string, args []string, diag *log.Logger) ([]string, bool) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			diag.Printf("lookout %s: %v\n%s", fs.Name(), err, usage)
			return nil, false
		}
		rest := fs.Args()
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
			pos = append(pos, rest...)
			break
		}
		if len(rest) == 0 {
			break
		}
		pos, args = append(pos, rest[0]), rest[1:]
	}
	settingGiven := false
	fs.Visit(func(f *flag.Flag) { settingGiven = settingGiven || f.Name == "setting" })
	if settingGiven && *setting == "" {
		diag.Printf("lookout %s: --setting needs a name\n%s", fs.Name(), usage)
		return nil, false
	}
	return pos, true
}
