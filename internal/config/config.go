// Package config loads one setting's configuration file: the inputs it
// expects and the workstreams the user has defined, in their stated order.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// defaultEventWindow bounds a workstream's events when the configuration does
// not: two weeks covers a typical iteration with room to see it start.
const defaultEventWindow = 14 * 24 * time.Hour

// namePattern matches supplied.v1's input name, which also keeps a setting name
// from naming a path outside the configuration directory.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Config is one setting.
type Config struct {
	Setting     string
	Path        string
	EventWindow time.Duration
	Inputs      []Input
	Workstreams []Workstream
}

// Input is one expected input. Supplied is an absolute path.
type Input struct {
	Name          string
	Supplied      string
	MaxAge        time.Duration
	ExpectRecords bool
}

// Workstream is one user-defined line of work and the items that belong to it.
type Workstream struct {
	Name    string
	Members []Member
}

// Member names one item by its input and its id within that input.
type Member struct {
	Input string
	ID    string
}

// Locate finds the one configuration file this invocation reads. A setting
// named by flag wins, then a path in LOOKOUT_CONFIG; otherwise the
// configuration directory must hold exactly one file, since several settings
// are never merged.
func Locate(getenv func(string) string, setting string) (string, error) {
	dir, err := dir(getenv)
	if setting != "" {
		if err != nil {
			return "", err
		}
		if !namePattern.MatchString(setting) {
			return "", fmt.Errorf("setting %q is not a valid name: use lowercase letters, digits, - and _", setting)
		}
		path := filepath.Join(dir, setting+".yml")
		if _, statErr := os.Stat(path); statErr != nil {
			return "", fmt.Errorf("no configuration for setting %q: %w", setting, statErr)
		}
		return path, nil
	}
	if path := getenv("LOOKOUT_CONFIG"); path != "" {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	found, err := settingFiles(dir)
	if err != nil {
		return "", err
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no configuration found: create %s, or set LOOKOUT_CONFIG to a file's path", filepath.Join(dir, "<setting>.yml"))
	case 1:
		return found[0], nil
	}
	names := make([]string, 0, len(found))
	for _, p := range found {
		names = append(names, settingName(p))
	}
	return "", fmt.Errorf("several settings are configured in %s (%s): choose one with --setting <name> or LOOKOUT_CONFIG", dir, strings.Join(names, ", "))
}

// settingFiles lists the configuration files in dir: regular *.yml files whose
// names do not start with a dot, so an editor's lock or backup file is never
// taken for a setting.
func settingFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", dir, err)
	}
	found := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.Type().IsRegular() && filepath.Ext(name) == ".yml" && !strings.HasPrefix(name, ".") {
			found = append(found, filepath.Join(dir, name))
		}
	}
	return found, nil
}

func dir(getenv func(string) string) (string, error) {
	if base := getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "lookout"), nil
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", "lookout"), nil
	}
	return "", errors.New("cannot find the configuration directory: neither XDG_CONFIG_HOME nor HOME is set")
}

func settingName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

type fileShape struct {
	EventWindow string      `yaml:"event_window"`
	Inputs      []inputFile `yaml:"inputs"`
	Workstreams []struct {
		Name    string   `yaml:"name"`
		Members []string `yaml:"members"`
	} `yaml:"workstreams"`
}

type inputFile struct {
	Name          string `yaml:"name"`
	Supplied      string `yaml:"supplied"`
	MaxAge        string `yaml:"max_age"`
	ExpectRecords bool   `yaml:"expect_records"`
}

// Load reads and checks the configuration at path. Every error names the file.
func Load(path string) (*Config, error) {
	cfg, err := load(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading configuration: %w", err)
	}
	var shape fileShape
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if decodeErr := dec.Decode(&shape); decodeErr != nil {
		return nil, fmt.Errorf("parsing configuration: %w", decodeErr)
	}

	cfg := &Config{Setting: settingName(path), Path: path, EventWindow: defaultEventWindow}
	if shape.EventWindow != "" {
		if cfg.EventWindow, err = duration("event_window", shape.EventWindow); err != nil {
			return nil, err
		}
		// The readout states the window in whole seconds, so a finer one would
		// filter by a bound the readout cannot report.
		if cfg.EventWindow%time.Second != 0 {
			return nil, fmt.Errorf("event_window %q must be a whole number of seconds", shape.EventWindow)
		}
	}

	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("resolving the configuration's directory: %w", err)
	}
	for _, in := range shape.Inputs {
		if !namePattern.MatchString(in.Name) {
			return nil, fmt.Errorf("input name %q is not valid: use lowercase letters, digits, - and _", in.Name)
		}
		if slices.ContainsFunc(cfg.Inputs, func(i Input) bool { return i.Name == in.Name }) {
			return nil, fmt.Errorf("input %q is listed twice", in.Name)
		}
		if in.Supplied == "" {
			return nil, fmt.Errorf("input %q needs supplied: the path of the file its supplier writes", in.Name)
		}
		if in.MaxAge == "" {
			return nil, fmt.Errorf("input %q needs max_age: how old its data may get before it counts as stale, such as 24h", in.Name)
		}
		maxAge, durErr := duration("input "+in.Name+" max_age", in.MaxAge)
		if durErr != nil {
			return nil, durErr
		}
		supplied := in.Supplied
		if !filepath.IsAbs(supplied) {
			supplied = filepath.Join(base, supplied)
		}
		cfg.Inputs = append(cfg.Inputs, Input{Name: in.Name, Supplied: supplied, MaxAge: maxAge, ExpectRecords: in.ExpectRecords})
	}

	for _, ws := range shape.Workstreams {
		if ws.Name == "" {
			return nil, errors.New("a workstream has no name")
		}
		if slices.ContainsFunc(cfg.Workstreams, func(w Workstream) bool { return w.Name == ws.Name }) {
			return nil, fmt.Errorf("workstream %q is listed twice", ws.Name)
		}
		w := Workstream{Name: ws.Name}
		for _, m := range ws.Members {
			input, id, ok := strings.Cut(m, ":")
			if !ok || id == "" {
				return nil, fmt.Errorf("workstream %q member %q must be <input>:<id>", ws.Name, m)
			}
			if !slices.ContainsFunc(cfg.Inputs, func(i Input) bool { return i.Name == input }) {
				return nil, fmt.Errorf("workstream %q member %q names input %q, which the configuration does not list", ws.Name, m, input)
			}
			member := Member{Input: input, ID: id}
			if slices.Contains(w.Members, member) {
				return nil, fmt.Errorf("workstream %q lists member %q twice", ws.Name, m)
			}
			w.Members = append(w.Members, member)
		}
		cfg.Workstreams = append(cfg.Workstreams, w)
	}

	// Naming an item of an input states that the input should have records,
	// whether or not expect_records says so.
	for i := range cfg.Inputs {
		for _, w := range cfg.Workstreams {
			if slices.ContainsFunc(w.Members, func(m Member) bool { return m.Input == cfg.Inputs[i].Name }) {
				cfg.Inputs[i].ExpectRecords = true
			}
		}
	}
	return cfg, nil
}

func duration(field, value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a duration such as 24h or 90m: %w", field, value, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s %q must not be negative", field, value)
	}
	return d, nil
}
