package config

import (
	"encoding/json"
	"os"
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
