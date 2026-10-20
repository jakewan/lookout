package main

import (
	"errors"
	"flag"
	"io"
	"log"

	"github.com/jakewan/lookout/internal/config"
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

func currentReadout(env environment, setting string) (readout.Readout, error) {
	// A second attempt finds the state and holds its lock, so a third is
	// reached only if the state keeps vanishing and reappearing.
	for range 3 {
		out, again, err := readOnce(env, setting)
		if err != nil || !again {
			return out, err
		}
	}
	return readout.Readout{}, errors.New("the state directory kept appearing while lookout read it: run the command again")
}

// readOnce builds the readout, or reports that it must be built again. A read
// of a setting with no state yet holds no lock, so where state appeared before
// the configuration was loaded, a gesture may have changed both, and the read
// starts over holding the lock. A gesture creates the state before it changes
// anything, so a read that finds none afterward saw no gesture at all.
func readOnce(env environment, setting string) (_ readout.Readout, again bool, err error) {
	loc, err := locate(env, setting, state.Read)
	if err != nil {
		return readout.Readout{}, false, err
	}
	defer func() { err = errors.Join(err, loc.store.Close()) }()
	afterStateOpen()
	cfg, err := config.Load(loc.path)
	if err != nil {
		return readout.Readout{}, false, err
	}
	if loc.store.Appeared() {
		return readout.Readout{}, true, nil
	}
	reader, err := supplied.NewReader()
	if err != nil {
		return readout.Readout{}, false, err
	}
	now := env.now()
	readings := make(map[string]supplied.Reading, len(cfg.Inputs))
	for _, in := range cfg.Inputs {
		readings[in.Name] = reader.Read(in, now)
	}
	return readout.Build(cfg, readings, loc.store.Dismissals(), now), false, nil
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
