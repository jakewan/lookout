package config

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// TestNamePatternMatchesTheContract pins the one rule config and supplied.v1
// state separately: an input name a configuration accepts must be one a
// supplier can write.
func TestNamePatternMatchesTheContract(t *testing.T) {
	data, err := os.ReadFile("../../schema/supplied.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Defs struct {
			Name struct {
				Pattern string `json:"pattern"`
			} `json:"name"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if got := namePattern.String(); got != doc.Defs.Name.Pattern {
		t.Errorf("namePattern = %q, supplied.v1 $defs.name.pattern = %q", got, doc.Defs.Name.Pattern)
	}
}

func TestParseReadsExplicitOnly(t *testing.T) {
	for text, want := range map[string]bool{
		"inputs: []\n":                          false,
		"explicit_only: false\ninputs: []\n":    false,
		"explicit_only: true\ninputs: []\n":     true,
		"explicit_only: &on true\ninputs: []\n": true,
		"inputs:\n  - {name: t, kind: supplied, supplied: t.json, max_age: 1h, expect_records: &on true}\nexplicit_only: *on\n": true,
	} {
		cfg, err := Parse([]byte(text), "personal.yml")
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		if cfg.ExplicitOnly != want {
			t.Errorf("%q: ExplicitOnly = %v, want %v", text, cfg.ExplicitOnly, want)
		}
	}
}

func TestParseRefusesAnExplicitOnlyThatIsNotABool(t *testing.T) {
	for _, text := range []string{
		"explicit_only: maybe\ninputs: []\n",
		"explicit_only: \"true\"\ninputs: []\n",
		"explicit_only: 1\ninputs: []\n",
		"explicit_only:\ninputs: []\n",
		"workstreams: &unset\nexplicit_only: *unset\ninputs: []\n",
	} {
		_, err := Parse([]byte(text), "personal.yml")
		if err == nil || !strings.Contains(err.Error(), "explicit_only") {
			t.Errorf("%q: err = %v, want a refusal naming explicit_only", text, err)
		}
	}
}

// TestKeysSearchesEveryIDButSearchSyntax pins which member ids a forge's
// search is asked for: every one, whatever the pattern would make of it, since
// each hit is checked against the key it carries, except one holding text the
// search would read as its own syntax rather than a key.
func TestKeysAllFindsEveryKeyOnce(t *testing.T) {
	cfg, err := Parse([]byte("key_pattern: '([A-Za-z]+-[0-9]+)'\n"), "personal.yml")
	if err != nil {
		t.Fatal(err)
	}
	keys := cfg.Keys()
	for text, want := range map[string][]string{
		"Ships ABC-1, XYZ-9 and abc-1 again": {"ABC-1", "XYZ-9"},
		"no key here":                        nil,
		"":                                   nil,
	} {
		if got := keys.All(text); !slices.Equal(got, want) {
			t.Errorf("All(%q) = %q, want %q", text, got, want)
		}
	}
	if got := (*Keys)(nil).All("ABC-1"); got != nil {
		t.Errorf("All on no keys = %q, want none", got)
	}
}

func TestKeysSearchesEveryIDButSearchSyntax(t *testing.T) {
	for name, tc := range map[string]struct {
		pattern string
		ids     []string
		want    []string
	}{
		"upper-case pattern, lower-case id": {`([A-Z]+-[0-9]+)`, []string{"abc-1", "ABC-2"}, []string{"ABC-2", "abc-1"}},
		"mixed-case pattern":                {`([A-Z][a-z]+-[0-9]+)`, []string{"ABC-1", "abc-2", "Abc-3"}, []string{"ABC-1", "Abc-3", "abc-2"}},
		"competing alternatives":            {`([a-z]+|[A-Z]+-[0-9]+)`, []string{"ABC-1"}, []string{"ABC-1"}},
		"an id the pattern cannot produce":  {`([A-Z]+-[0-9]+)`, []string{"ABC-1", "notes"}, []string{"ABC-1", "notes"}},
		"search syntax in an id":            {`(\S+)`, []string{`ABC-1`, `a"b`, `x:y`, `a(b`, `a\b`, "a b"}, []string{"ABC-1"}},
	} {
		t.Run(name, func(t *testing.T) {
			members := make([]string, 0, len(tc.ids))
			for _, id := range tc.ids {
				members = append(members, "      - '"+strings.ReplaceAll("tracker:"+id, "'", "''")+"'")
			}
			text := "key_pattern: '" + tc.pattern + "'\ninputs:\n  - {name: tracker, kind: supplied, supplied: t.json, max_age: 1h}\nworkstreams:\n  - name: w\n    members:\n" + strings.Join(members, "\n") + "\n"
			cfg, err := Parse([]byte(text), "personal.yml")
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Keys().Searchable(); !slices.Equal(got, tc.want) {
				t.Errorf("Searchable = %q, want %q", got, tc.want)
			}
		})
	}
}
