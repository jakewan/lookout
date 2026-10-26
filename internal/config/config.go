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
	"unicode"

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
	Setting string
	Path    string
	// ExplicitOnly is the file's explicit_only. Locate reads the key from the
	// file itself, before the file is loaded; the field is here so that edits
	// check it with everything else they must leave as it was.
	ExplicitOnly bool
	EventWindow  time.Duration
	// DiscoveryWindow bounds how long ago an item a built-in input finds may
	// have moved and still be offered as unplaced.
	DiscoveryWindow time.Duration
	// ForgeOwners are the forge owners whose work this setting admits.
	ForgeOwners []Owner
	// KeyPattern is the file's key_pattern, as written, or empty for none.
	// Keys reads it; it is kept as text so edits compare it as they compare
	// everything else.
	KeyPattern  string
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
	// KindGitHub is pull requests and issues on github.com, read live.
	KindGitHub = "github"
	// KindClaudeCode is the session transcripts Claude Code keeps in its
	// configuration directories, read live.
	KindClaudeCode = "claude_code"
)

// Input is one expected input. Supplied is an absolute path. Repos are paths or
// glob patterns, and ConfigDirs paths, exactly as the configuration writes
// them, so that health can name an entry the way the user wrote it; a reader
// expands a leading ~/ and reads a relative entry from Dir, the
// configuration's directory.
type Input struct {
	Name           string
	Kind           string
	Supplied       string
	MaxAge         time.Duration
	ExpectRecords  bool
	Repos          []string
	IgnoreBranches []string
	ConfigDirs     []string
	Dir            string
	// expectWritten is expect_records as the file writes it, apart from what
	// membership adds, so an edit that removes the last member can tell
	// whether the expectation stays.
	expectWritten bool
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

// keyExample is a key_pattern refusals offer, single-quoted so YAML reads a
// leading [ and a backslash as part of the pattern.
const keyExample = `key_pattern: '([A-Z][A-Z0-9]+-[0-9]+)'`

// Keys finds the key work names its ticket by, and the live workstream whose
// supplied member is that ticket.
type Keys struct {
	pattern *regexp.Regexp
	// owners maps each key, lowercased, to the member it is the id of.
	owners map[string]KeyOwner
	// searchable is each owned key, as the configuration writes it, that the
	// pattern finds in itself.
	searchable []string
}

// KeyOwner is the workstream a key places work in, and the member the key is
// the id of.
type KeyOwner struct {
	Workstream string
	Member     Member
}

// Keys is the setting's keys, or nil where it configures no key_pattern. A key
// belongs to the first live workstream, in the order the configuration states
// them, that names a supplied member with that id; ids are compared without
// regard to case, as trackers compare keys.
func (c *Config) Keys() *Keys {
	if c.KeyPattern == "" {
		return nil
	}
	// Parse refused a pattern that does not compile, so only a Config built by
	// hand can fail here, and it then has no keys.
	pattern, err := regexp.Compile(c.KeyPattern)
	if err != nil {
		return nil
	}
	k := &Keys{pattern: pattern, owners: map[string]KeyOwner{}}
	for _, w := range c.Workstreams {
		if w.Completed != "" {
			continue
		}
		for _, m := range w.Members {
			if !slices.ContainsFunc(c.Inputs, func(i Input) bool { return i.Name == m.Input && i.Kind == KindSupplied }) {
				continue
			}
			key := strings.ToLower(m.ID)
			if _, taken := k.owners[key]; taken {
				continue
			}
			k.owners[key] = KeyOwner{Workstream: w.Name, Member: m}
			if k.searches(m.ID) {
				k.searchable = append(k.searchable, m.ID)
			}
		}
	}
	slices.Sort(k.searchable)
	return k
}

// Of is the key the first of texts to carry one carries: the pattern's capture
// in its first match. It is empty where none does, and on nil Keys.
func (k *Keys) Of(texts ...string) string {
	if k == nil {
		return ""
	}
	for _, t := range texts {
		if m := k.pattern.FindStringSubmatch(t); m != nil && m[1] != "" {
			return m[1]
		}
	}
	return ""
}

// Owner is the workstream key places work in.
func (k *Keys) Owner(key string) (KeyOwner, bool) {
	if k == nil || key == "" {
		return KeyOwner{}, false
	}
	o, ok := k.owners[strings.ToLower(key)]
	return o, ok
}

// Places reports whether any key places work in the named workstream.
func (k *Keys) Places(workstream string) bool {
	if k == nil {
		return false
	}
	for _, o := range k.owners {
		if o.Workstream == workstream {
			return true
		}
	}
	return false
}

// searches reports whether id is worth asking a forge's search for: the
// pattern finds it in itself in some spelling, as keys compare without regard
// to case, and it holds nothing a search would read as its own syntax, such as
// a quote, a qualifier's colon or a space.
func (k *Keys) searches(id string) bool {
	if strings.ContainsAny(id, `"():\`) || strings.ContainsFunc(id, unicode.IsSpace) {
		return false
	}
	for _, spelling := range []string{id, strings.ToUpper(id), strings.ToLower(id)} {
		if strings.EqualFold(k.Of(spelling), id) {
			return true
		}
	}
	return false
}

// Searchable is each key worth asking a forge's search for, so an id no work
// could carry, and one holding search syntax, is never sent.
func (k *Keys) Searchable() []string {
	if k == nil {
		return nil
	}
	return k.searchable
}

// Locate finds the one configuration file this invocation reads. A setting
// named by flag wins, then a path in LOOKOUT_CONFIG; otherwise the
// configuration directory must hold exactly one file, since several settings
// are never merged, and that file must not be explicit_only.
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
		// The refusal comes before anything opens the setting's state, so a
		// command meant for another setting leaves no trace in this one.
		if refuseErr := chosenByDefault(found[0]); refuseErr != nil {
			return "", refuseErr
		}
		return found[0], nil
	}
	names := make([]string, 0, len(found))
	for _, p := range found {
		names = append(names, strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
	}
	return "", fmt.Errorf("several settings are configured in %s (%s): choose one with --setting <name> or LOOKOUT_CONFIG", dir, strings.Join(names, ", "))
}

// explicitOnlyLine finds a line that looks like it sets explicit_only, however
// indented, quoted, cased or spelled with - or no separator. chosenByDefault
// falls back to it wherever the parsed file holds no top-level explicit_only,
// so a file whose user may have meant to mark it is not chosen by default.
var explicitOnlyLine = regexp.MustCompile(`(?mi)^[ \t\x{FEFF}]*["']?explicit[-_]?only["']?[ \t]*:`)

// chosenByDefault refuses the only file in the configuration directory when it
// is, or may be, explicit_only. It reads the key as parse does, but nothing
// else, so a file that parses but is wrong in any other way is still chosen and
// its error reported, as it would be without the key. An unreadable file, and
// one whose name no route accepts, are chosen for the same reason: whatever
// reads it next names the failure, before any state is opened for it.
func chosenByDefault(path string) error {
	name, nameErr := settingName(path)
	if nameErr != nil {
		return nil
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return nil
	}
	var shape struct {
		ExplicitOnly yaml.Node `yaml:"explicit_only"`
	}
	var explicit bool
	readErr = yaml.Unmarshal(data, &shape)
	switch {
	case readErr == nil && shape.ExplicitOnly.Kind != 0:
		explicit, readErr = explicitOnly(shape.ExplicitOnly)
	case !explicitOnlyLine.Match(data):
		return nil
	default:
		readErr = errors.New("explicit_only is not set as lookout reads it")
	}
	routes := "--setting " + name + " or LOOKOUT_CONFIG=" + path
	switch {
	case readErr != nil:
		return fmt.Errorf("%s looks like it sets explicit_only, but lookout cannot read it as a top-level key set to a boolean, so it is never chosen by default: check its spelling and value, then name it with %s", path, routes)
	case explicit:
		return fmt.Errorf("%s sets explicit_only, so it is never chosen by default: name it with %s", path, routes)
	}
	return nil
}

// explicitOnly reads explicit_only as yaml.v3 decodes a bool, refusing an empty
// value, which would otherwise read as false and leave a setting its user meant
// to mark open to being chosen by default.
func explicitOnly(n yaml.Node) (bool, error) {
	if n.Kind == 0 {
		return false, nil
	}
	// An alias carries none of its target's tag, so an alias to null would
	// otherwise decode as false without error.
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = *n.Alias
	}
	var explicit bool
	if n.Tag == "!!null" || n.Decode(&explicit) != nil {
		return false, fmt.Errorf("explicit_only %q is not a boolean such as true or false", n.Value)
	}
	return explicit, nil
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
	ExplicitOnly    yaml.Node   `yaml:"explicit_only"`
	EventWindow     string      `yaml:"event_window"`
	DiscoveryWindow string      `yaml:"discovery_window"`
	ForgeOwners     []string    `yaml:"forge_owners"`
	KeyPattern      string      `yaml:"key_pattern"`
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
	ConfigDirs     []string `yaml:"config_dirs"`
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
	if cfg.ExplicitOnly, err = explicitOnly(shape.ExplicitOnly); err != nil {
		return nil, err
	}
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
	if err = keyPattern(shape.KeyPattern); err != nil {
		return nil, err
	}
	cfg.KeyPattern = shape.KeyPattern

	for _, in := range shape.Inputs {
		if !namePattern.MatchString(in.Name) {
			return nil, fmt.Errorf("input name %q is not valid: use lowercase letters, digits, - and _", in.Name)
		}
		if slices.ContainsFunc(cfg.Inputs, func(i Input) bool { return i.Name == in.Name }) {
			return nil, fmt.Errorf("input %q is listed twice", in.Name)
		}
		switch in.Kind {
		case "":
			return nil, fmt.Errorf("input %q needs kind: supplied, for a file an outside tool writes, git, for local repositories, github, for pull requests and issues on github.com, or claude_code, for Claude Code sessions", in.Name)
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
		case KindGitHub:
			hub, hubErr := gitHubInput(in, cfg.ForgeOwners)
			if hubErr != nil {
				return nil, hubErr
			}
			cfg.Inputs = append(cfg.Inputs, hub)
			continue
		case KindClaudeCode:
			sessions, sessionsErr := claudeCodeInput(in, base)
			if sessionsErr != nil {
				return nil, sessionsErr
			}
			if len(cfg.ForgeOwners) == 0 {
				return nil, fmt.Errorf("forge_owners is required once a claude_code input is configured: list the owners whose repositories this setting admits sessions from, such as forge_owners: [your-name]")
			}
			cfg.Inputs = append(cfg.Inputs, sessions)
			continue
		default:
			return nil, fmt.Errorf("input %q kind %q is not one lookout reads: use supplied, git, github or claude_code", in.Name, in.Kind)
		}
		if len(in.Repos) > 0 {
			return nil, fmt.Errorf("input %q is kind supplied, so repos applies only to kind git: remove it", in.Name)
		}
		if len(in.IgnoreBranches) > 0 {
			return nil, fmt.Errorf("input %q is kind supplied, so ignore_branches applies only to kinds git and claude_code: remove it", in.Name)
		}
		if len(in.ConfigDirs) > 0 {
			return nil, fmt.Errorf("input %q is kind supplied, so config_dirs applies only to kind claude_code: remove it", in.Name)
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
		cfg.Inputs = append(cfg.Inputs, Input{Name: in.Name, Kind: in.Kind, Supplied: supplied, MaxAge: maxAge, ExpectRecords: in.ExpectRecords, expectWritten: in.ExpectRecords})
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

	cfg.implyExpectations()
	return cfg, nil
}

// implyExpectations sets each supplied input's ExpectRecords from what the file
// writes and from its membership: naming an item of a supplied input states
// that the input should have records, whether or not expect_records says so.
// An input lookout reads itself is asked for its members by name, so one that
// has gone, such as a deleted branch, already shows in members_unread.
func (c *Config) implyExpectations() {
	for i := range c.Inputs {
		in := &c.Inputs[i]
		if in.Kind != KindSupplied {
			continue
		}
		in.ExpectRecords = in.expectWritten || slices.ContainsFunc(c.Workstreams, func(w Workstream) bool {
			return slices.ContainsFunc(w.Members, func(m Member) bool { return m.Input == in.Name })
		})
	}
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
	if len(in.ConfigDirs) > 0 {
		return Input{}, fmt.Errorf("input %q is kind git, so config_dirs applies only to kind claude_code: remove it", in.Name)
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
	if err := branchPatterns(in); err != nil {
		return Input{}, err
	}
	return Input{Name: in.Name, Kind: KindGit, ExpectRecords: in.ExpectRecords, Repos: repos, IgnoreBranches: in.IgnoreBranches, Dir: base}, nil
}

// gitHubInput checks a github input, which takes no keys of its own and reads
// only the owners forge_owners lists on github.com.
func gitHubInput(in inputFile, owners []Owner) (Input, error) {
	live := "input %q is kind github, which lookout reads live"
	switch {
	case in.Supplied != "":
		return Input{}, fmt.Errorf(live+" rather than from a file: remove supplied", in.Name)
	case in.MaxAge != "":
		return Input{}, fmt.Errorf(live+", so it is never stale: remove max_age", in.Name)
	case len(in.Repos) > 0:
		return Input{}, fmt.Errorf("input %q is kind github, so repos applies only to kind git: remove it", in.Name)
	case len(in.IgnoreBranches) > 0:
		return Input{}, fmt.Errorf("input %q is kind github, so ignore_branches applies only to kinds git and claude_code: remove it", in.Name)
	case len(in.ConfigDirs) > 0:
		return Input{}, fmt.Errorf("input %q is kind github, so config_dirs applies only to kind claude_code: remove it", in.Name)
	case len(owners) == 0:
		return Input{}, fmt.Errorf("forge_owners is required once a github input is configured: list the owners whose pull requests and issues this setting admits, such as forge_owners: [your-name]")
	case !slices.ContainsFunc(owners, func(o Owner) bool { return o.Host == defaultForgeHost }):
		return Input{}, fmt.Errorf("input %q: a github input needs a github.com owner in forge_owners, written without a host, such as forge_owners: [your-name]", in.Name)
	}
	return Input{Name: in.Name, Kind: KindGitHub, ExpectRecords: in.ExpectRecords}, nil
}

// claudeCodeInput checks a claude_code input's keys. A relative config_dirs
// entry is read from the configuration's directory, as a repos entry is.
func claudeCodeInput(in inputFile, base string) (Input, error) {
	live := "input %q is kind claude_code, which lookout reads live"
	switch {
	case in.Supplied != "":
		return Input{}, fmt.Errorf(live+" rather than from a file: remove supplied", in.Name)
	case in.MaxAge != "":
		return Input{}, fmt.Errorf(live+", so it is never stale: remove max_age", in.Name)
	case len(in.Repos) > 0:
		return Input{}, fmt.Errorf("input %q is kind claude_code, so repos applies only to kind git: remove it", in.Name)
	case len(in.ConfigDirs) == 0:
		return Input{}, fmt.Errorf("input %q needs config_dirs: the Claude Code configuration directories whose sessions to read, such as [~/.claude]", in.Name)
	case slices.Contains(in.ConfigDirs, ""):
		return Input{}, fmt.Errorf("input %q has an empty config_dirs entry", in.Name)
	}
	if err := branchPatterns(in); err != nil {
		return Input{}, err
	}
	return Input{Name: in.Name, Kind: KindClaudeCode, ExpectRecords: in.ExpectRecords, ConfigDirs: in.ConfigDirs, IgnoreBranches: in.IgnoreBranches, Dir: base}, nil
}

// branchPatterns checks an input's ignore_branches, which match a branch name
// as path.Match does, so * stops at a slash.
func branchPatterns(in inputFile) error {
	for _, p := range in.IgnoreBranches {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("input %q ignore_branches entry %q is not a valid pattern: %w", in.Name, p, err)
		}
	}
	return nil
}

// gitHubLogin is the shape of a github.com user or organization name, as
// GitHub's sign-up form allows them.
var gitHubLogin = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}$`)

// keyPattern checks key_pattern, where one is written: the key is what its one
// capture group captures, and a pattern that matches an empty text would find
// a key in every text it is given.
func keyPattern(source string) error {
	if source == "" {
		return nil
	}
	pattern, err := regexp.Compile(source)
	if err != nil {
		return fmt.Errorf("key_pattern %q is not a regular expression lookout reads (%w): write it in single quotes, such as %s", source, err, keyExample)
	}
	if n := pattern.NumSubexp(); n != 1 {
		return fmt.Errorf("key_pattern %q has %d capture groups: put exactly one around the key, and write any other group as (?:...), such as %s", source, n, keyExample)
	}
	if pattern.MatchString("") {
		return fmt.Errorf("key_pattern %q matches an empty text, so it would find a key in everything: make the key required, such as %s", source, keyExample)
	}
	return nil
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
		if host == "" || name == "" || strings.Contains(name, "/") || strings.IndexFunc(e, unicode.IsSpace) >= 0 {
			return nil, fmt.Errorf("forge_owners entry %q is not an owner: write it as your-name for github.com, or host/your-name for another host", e)
		}
		o := Owner{Host: strings.ToLower(host), Name: strings.ToLower(name)}
		// A github.com owner is written into search queries, where any other
		// character could start a qualifier of its own.
		if o.Host == defaultForgeHost && !gitHubLogin.MatchString(o.Name) {
			return nil, fmt.Errorf("forge_owners entry %q is not a github.com account name: use letters, digits and -", e)
		}
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
