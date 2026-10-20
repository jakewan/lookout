package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/readout"
	"github.com/jakewan/lookout/internal/state"
	"github.com/jakewan/lookout/internal/supplied"
)

// read writes one setting's readout to stdout. A readout whose inputs are
// unhealthy still succeeds: reporting that is its job.
func read(args []string, env environment, stdout io.Writer, diag *log.Logger) int {
	fs, setting := newFlags("read")
	asJSON := fs.Bool("json", false, "")
	pos, ok := parseFlags(fs, setting, args, diag)
	if !ok {
		return exitUsage
	}
	if len(pos) > 0 {
		diag.Printf("lookout read: unexpected argument %q\n%s", pos[0], usage)
		return exitUsage
	}
	if !*asJSON {
		diag.Printf("lookout read: --json is required; JSON is the only output so far\n%s", usage)
		return exitUsage
	}

	out, err := currentReadout(env, *setting)
	if err != nil {
		diag.Printf("lookout read: %v", err)
		return exitFailure
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		diag.Printf("lookout read: writing the readout: %v", err)
		return exitFailure
	}
	return 0
}

// unplaced lists the readout's unplaced items one per line, each starting with
// the token adopt and dismiss take, so a person can act on one without reading
// JSON.
func unplaced(args []string, env environment, stdout io.Writer, diag *log.Logger) int {
	fs, setting := newFlags("unplaced")
	pos, ok := parseFlags(fs, setting, args, diag)
	if !ok {
		return exitUsage
	}
	if len(pos) > 0 {
		diag.Printf("lookout unplaced: unexpected argument %q\n%s", pos[0], usage)
		return exitUsage
	}
	out, err := currentReadout(env, *setting)
	if err != nil {
		diag.Printf("lookout unplaced: %v", err)
		return exitFailure
	}
	// Tabs and line breaks in a title would split its line.
	flat := strings.NewReplacer("\t", " ", "\r", " ", "\n", " ")
	for _, p := range out.Unplaced {
		var it struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			LastTouched string `json:"last_touched"`
		}
		if err := json.Unmarshal(p.Item, &it); err != nil {
			diag.Printf("lookout unplaced: reading an item of %s: %v", p.Input, err)
			return exitFailure
		}
		if _, err := fmt.Fprintf(stdout, "%s:%s\t%s\t%s\n", p.Input, it.ID, flat.Replace(it.Title), it.LastTouched); err != nil {
			diag.Printf("lookout unplaced: %v", err)
			return exitFailure
		}
	}
	return 0
}

// located is the setting an invocation acts on.
type located struct {
	path  string
	store *state.Store
}

func locate(env environment, setting string) (*located, error) {
	path, err := config.Locate(env.getenv, setting)
	if err != nil {
		return nil, err
	}
	name, err := config.Name(path)
	if err != nil {
		return nil, err
	}
	store, err := state.Open(env.getenv, name, path)
	if err != nil {
		return nil, err
	}
	return &located{path: path, store: store}, nil
}

func currentReadout(env environment, setting string) (readout.Readout, error) {
	loc, err := locate(env, setting)
	if err != nil {
		return readout.Readout{}, err
	}
	cfg, err := config.Load(loc.path)
	if err != nil {
		return readout.Readout{}, err
	}
	reader, err := supplied.NewReader()
	if err != nil {
		return readout.Readout{}, err
	}
	now := env.now()
	readings := make(map[string]supplied.Reading, len(cfg.Inputs))
	for _, in := range cfg.Inputs {
		readings[in.Name] = reader.Read(in, now)
	}
	return readout.Build(cfg, readings, loc.store.Dismissals(), now), nil
}

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
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos, args = append(pos, args[0]), args[1:]
	}
	settingGiven := false
	fs.Visit(func(f *flag.Flag) { settingGiven = settingGiven || f.Name == "setting" })
	if settingGiven && *setting == "" {
		diag.Printf("lookout %s: --setting needs a name\n%s", fs.Name(), usage)
		return nil, false
	}
	return pos, true
}
