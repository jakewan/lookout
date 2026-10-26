package github

import (
	"slices"
	"strings"
	"testing"
)

// TestKeyGroupsStayWithinWhatOneSearchAccepts pins how keys are split across
// searches: each group within keysPerSearch keys and keyText characters as the
// query quotes and joins them, and a key no search could hold left out.
func TestKeyGroupsStayWithinWhatOneSearchAccepts(t *testing.T) {
	long := strings.Repeat("A", keyText-len(`""`))
	tooLong := long + "A"
	half := strings.Repeat("B", (keyText-len(`"" OR ""`))/2)
	for name, tc := range map[string]struct {
		keys    []string
		groups  [][]string
		skipped []string
	}{
		"none":                {nil, nil, nil},
		"one group":           {[]string{"K-1", "K-2"}, [][]string{{"K-1", "K-2"}}, nil},
		"a full group":        {[]string{"K-1", "K-2", "K-3", "K-4", "K-5", "K-6"}, [][]string{{"K-1", "K-2", "K-3", "K-4", "K-5", "K-6"}}, nil},
		"past the key limit":  {[]string{"K-1", "K-2", "K-3", "K-4", "K-5", "K-6", "K-7"}, [][]string{{"K-1", "K-2", "K-3", "K-4", "K-5", "K-6"}, {"K-7"}}, nil},
		"at the text limit":   {[]string{half, half}, [][]string{{half, half}}, nil},
		"past the text limit": {[]string{half, half + "C"}, [][]string{{half}, {half + "C"}}, nil},
		"a key alone at most": {[]string{long}, [][]string{{long}}, nil},
		"a key too long":      {[]string{"K-1", tooLong, "K-2"}, [][]string{{"K-1", "K-2"}}, []string{tooLong}},
	} {
		t.Run(name, func(t *testing.T) {
			groups, skipped := keyGroups(tc.keys)
			if !slices.EqualFunc(groups, tc.groups, slices.Equal[[]string]) {
				t.Errorf("groups = %q, want %q", groups, tc.groups)
			}
			if !slices.Equal(skipped, tc.skipped) {
				t.Errorf("skipped = %q, want %q", skipped, tc.skipped)
			}
			for _, g := range groups {
				if len(g) > keysPerSearch {
					t.Errorf("group %q has more than %d keys", g, keysPerSearch)
				}
				if n := len(`"` + strings.Join(g, `" OR "`) + `"`); n > keyText {
					t.Errorf("group %q is %d characters, more than %d", g, n, keyText)
				}
			}
		})
	}
}
