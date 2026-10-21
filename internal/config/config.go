// Package config loads one setting's configuration file: the inputs it
// expects and the workstreams the user has defined, in their stated order.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
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
	// DiscoveryWindow bounds how long ago an item a built-in input finds may
	// have moved and still be offered as unplaced.
	DiscoveryWindow time.Duration
	// ForgeOwners are the forge owners whose work this setting admits.
	ForgeOwners []Owner
	Inputs      []Input
	Workstreams []Workstream
}

// Owner is one forge owner a setting admits: an account or organization on a
// host, both lowercase, since forges compare them without regard to case.
type Owner struct {
	Host string
	Name string
}

// defaultForgeHost is the host a forge_owners entry written without one names.
const defaultForgeHost = "github.com"

// Input kinds, as a configuration's kind key names them.
const (
	// KindSupplied is a file an outside supplier writes in the supplied.v1
	// contract.
	KindSupplied = "supplied"
	// KindGit is the local branches of git repositories, read live.
	KindGit = "git"
)

// Input is one expected input. Supplied is an absolute path. Repos are paths or
// glob patterns exactly as the configuration writes them, so that health can
// name an entry the way the user wrote it; a git reader expands a leading ~/
// and reads a relative entry from Dir, the configuration's directory.
type Input struct {
	Name           string
	Kind           string
	Supplied       string
	MaxAge         time.Duration
	ExpectRecords  bool
	Repos          []string
	IgnoreBranches []string
	Dir            string
}

// Workstream is one user-defined line of work and the items that belong to it.
// Completed is the local date, as YYYY-MM-DD, the user marked it finished, or
// empty while it is live.
type Workstream struct {
	Name      string
	Members   []Member
	Completed string
}

// dateLayout is how configuration dates are written: a calendar date with no
// zone, read in the zone of the clock lookout runs under.
const dateLayout = "2006-01-02"

// CompletedThrough is the instant a completed workstream's date ends in loc:
// an item touched after it has moved since the workstream was finished.
func (w Workstream) CompletedThrough(loc *time.Location) time.Time {
	day, err := time.ParseInLocation(dateLayout, w.Completed, loc)
	if err != nil {
		// Parse rejects an invalid date, so only a Workstream built by hand reaches
		// here; treating it as never ending keeps its members claimed.
		return time.Time{}
	}
	return day.AddDate(0, 0, 1)
}

// MemberIDs is the ids of input's items that a live workstream names. A
// completed workstream's item is offered again only as discovery would offer
// any other, so it is left out.
func (c *Config) MemberIDs(input string) map[string]bool {
	ids := map[string]bool{}
	for _, w := range c.Workstreams {
		if w.Completed != "" {
			continue
		}
		for _, m := range w.Members {
			if m.Input == input {
				ids[m.ID] = true
			}
		}
	}
	return ids
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
		info, statErr := os.Stat(path)
		if statErr != nil {
			return "", fmt.Errorf("no configuration for setting %q: %w", setting, statErr)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("the configuration for setting %q, %s, is not a file", setting, path)
		}
		return path, nil
	}
	if path := getenv("LOOKOUT_CONFIG"); path != "" {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return "", fmt.Errorf("LOOKOUT_CONFIG names %s: %w", path, statErr)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("LOOKOUT_CONFIG names %s, which is not a file", path)
		}
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
		names = append(names, strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
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

// Name is the setting a configuration file at path configures: its file name
// without the extension. It must be a valid name whichever route located the
// file, because state is kept per setting name.
func Name(path string) (string, error) {
	name, err := settingName(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return name, nil
}

func settingName(path string) (string, error) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("%q is not a valid setting name: name the file with lowercase letters, digits, - and _, such as personal.yml", name)
	}
	return name, nil
}

type fileShape struct {
	EventWindow     string      `yaml:"event_window"`
	DiscoveryWindow string      `yaml:"discovery_window"`
	ForgeOwners     []string    `yaml:"forge_owners"`
	Inputs          []inputFile `yaml:"inputs"`
	Workstreams     []struct {
		Name      string   `yaml:"name"`
		Members   []string `yaml:"members"`
		Completed string   `yaml:"completed"`
	} `yaml:"workstreams"`
}

type inputFile struct {
	Name           string   `yaml:"name"`
	Kind           string   `yaml:"kind"`
	Supplied       string   `yaml:"supplied"`
	MaxAge         string   `yaml:"max_age"`
	ExpectRecords  bool     `yaml:"expect_records"`
	Repos          []string `yaml:"repos"`
	IgnoreBranches []string `yaml:"ignore_branches"`
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
	return parse(data, path)
}

// Parse checks configuration text as if it were read from path, which names
// the setting and anchors relative supplied paths. Every error names path.
func Parse(data []byte, path string) (*Config, error) {
	cfg, err := parse(data, path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func parse(data []byte, path string) (*Config, error) {
	setting, err := settingName(path)
	if err != nil {
		return nil, err
	}
	var shape fileShape
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if decodeErr := dec.Decode(&shape); decodeErr != nil {
		return nil, fmt.Errorf("parsing configuration: %w", decodeErr)
	}

	cfg := &Config{Setting: setting, Path: path, EventWindow: defaultEventWindow}
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
	cfg.DiscoveryWindow = cfg.EventWindow
	if shape.DiscoveryWindow != "" {
		if cfg.DiscoveryWindow, err = duration("discovery_window", shape.DiscoveryWindow); err != nil {
			return nil, err
		}
	}
	if cfg.ForgeOwners, err = owners(shape.ForgeOwners); err != nil {
		return nil, err
	}

	for _, in := range shape.Inputs {
		if !namePattern.MatchString(in.Name) {
			return nil, fmt.Errorf("input name %q is not valid: use lowercase letters, digits, - and _", in.Name)
		}
		if slices.ContainsFunc(cfg.Inputs, func(i Input) bool { return i.Name == in.Name }) {
			return nil, fmt.Errorf("input %q is listed twice", in.Name)
		}
		switch in.Kind {
		case "":
			return nil, fmt.Errorf("input %q needs kind: supplied, for a file an outside tool writes, or git, for local repositories", in.Name)
		case KindSupplied:
		case KindGit:
			git, gitErr := gitInput(in, base)
			if gitErr != nil {
				return nil, gitErr
			}
			if len(cfg.ForgeOwners) == 0 {
				return nil, fmt.Errorf("forge_owners is required once a git input is configured: list the owners whose repositories this setting admits, such as forge_owners: [your-name]")
			}
			cfg.Inputs = append(cfg.Inputs, git)
			continue
		default:
			return nil, fmt.Errorf("input %q kind %q is not one lookout reads: use supplied or git", in.Name, in.Kind)
		}
		if len(in.Repos) > 0 {
			return nil, fmt.Errorf("input %q is kind supplied, so repos applies only to kind git: remove it", in.Name)
		}
		if len(in.IgnoreBranches) > 0 {
			return nil, fmt.Errorf("input %q is kind supplied, so ignore_branches applies only to kind git: remove it", in.Name)
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
		cfg.Inputs = append(cfg.Inputs, Input{Name: in.Name, Kind: in.Kind, Supplied: supplied, MaxAge: maxAge, ExpectRecords: in.ExpectRecords})
	}

	for _, ws := range shape.Workstreams {
		if ws.Name == "" {
			return nil, errors.New("a workstream has no name")
		}
		if slices.ContainsFunc(cfg.Workstreams, func(w Workstream) bool { return w.Name == ws.Name }) {
			return nil, fmt.Errorf("workstream %q is listed twice", ws.Name)
		}
		w := Workstream{Name: ws.Name, Completed: ws.Completed}
		if ws.Completed != "" {
			if _, dateErr := time.Parse(dateLayout, ws.Completed); dateErr != nil {
				return nil, fmt.Errorf("workstream %q completed %q is not a date such as 2030-06-15", ws.Name, ws.Completed)
			}
		}
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

	// Naming an item of a supplied input states that the input should have
	// records, whether or not expect_records says so. A git member's branch
	// ends its life by being deleted, which members_unread already shows.
	for i := range cfg.Inputs {
		if cfg.Inputs[i].Kind != KindSupplied {
			continue
		}
		for _, w := range cfg.Workstreams {
			if slices.ContainsFunc(w.Members, func(m Member) bool { return m.Input == cfg.Inputs[i].Name }) {
				cfg.Inputs[i].ExpectRecords = true
			}
		}
	}
	return cfg, nil
}

// gitInput checks a git input's keys. A relative repos entry is read from the
// configuration's directory, as a supplied path is.
func gitInput(in inputFile, base string) (Input, error) {
	if in.Supplied != "" {
		return Input{}, fmt.Errorf("input %q is kind git, which lookout reads live rather than from a file: remove supplied", in.Name)
	}
	if in.MaxAge != "" {
		return Input{}, fmt.Errorf("input %q is kind git, which lookout reads live, so it is never stale: remove max_age", in.Name)
	}
	if len(in.Repos) == 0 {
		return Input{}, fmt.Errorf("input %q needs repos: the repository paths or patterns to read, such as ~/code/*", in.Name)
	}
	repos := make([]string, 0, len(in.Repos))
	for _, r := range in.Repos {
		// filepath.Glob reads ** as two * within one name, never as any depth.
		if strings.Contains(r, "**") {
			return Input{}, fmt.Errorf("input %q repos entry %q uses **, which matches within one directory name only: list each depth, such as ~/code/* and ~/code/*/*", in.Name, r)
		}
		if _, err := filepath.Match(r, ""); err != nil {
			return Input{}, fmt.Errorf("input %q repos entry %q is not a valid pattern: %w", in.Name, r, err)
		}
		if r == "" {
			return Input{}, fmt.Errorf("input %q has an empty repos entry", in.Name)
		}
		repos = append(repos, r)
	}
	for _, p := range in.IgnoreBranches {
		if _, err := path.Match(p, ""); err != nil {
			return Input{}, fmt.Errorf("input %q ignore_branches entry %q is not a valid pattern: %w", in.Name, p, err)
		}
	}
	return Input{Name: in.Name, Kind: KindGit, ExpectRecords: in.ExpectRecords, Repos: repos, IgnoreBranches: in.IgnoreBranches, Dir: base}, nil
}

// owners reads forge_owners. An entry is an owner on github.com, or
// host/owner for any other host.
func owners(entries []string) ([]Owner, error) {
	out := make([]Owner, 0, len(entries))
	for _, e := range entries {
		host, name, ok := strings.Cut(e, "/")
		if !ok {
			host, name = defaultForgeHost, e
		}
		if host == "" || name == "" || strings.ContainsAny(name, "/ \t") || strings.ContainsAny(host, " \t") {
			return nil, fmt.Errorf("forge_owners entry %q is not an owner: write it as your-name for github.com, or host/your-name for another host", e)
		}
		o := Owner{Host: strings.ToLower(host), Name: strings.ToLower(name)}
		if slices.Contains(out, o) {
			return nil, fmt.Errorf("forge_owners lists %s/%s twice", o.Host, o.Name)
		}
		out = append(out, o)
	}
	return out, nil
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
