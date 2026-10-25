package config

import (
	"encoding/json"
	"os"
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
