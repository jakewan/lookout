package baseline

import (
	"strings"
	"testing"

	"github.com/jakewan/lookout/internal/config"
)

// digestFixture is a setting with no key pattern. Its digest is pinned so that
// a field added to what the digest covers cannot change it for settings that
// do not use the field, which would report discovery_changed on the first
// snapshot after an upgrade.
const digestFixture = `
discovery_window: 72h
forge_owners: [alice]
inputs:
  - name: tracker
    kind: supplied
    supplied: tracker.json
    max_age: 24h
  - name: code
    kind: git
    repos: ["/code/*"]
  - name: hub
    kind: github
workstreams:
  - name: billing
    members: ["tracker:ABC-1"]
`

func TestDiscoveryDigestOfASettingWithoutAKeyPatternIsUnchanged(t *testing.T) {
	cfg, err := config.Parse([]byte(digestFixture), "/settings/personal.yml")
	if err != nil {
		t.Fatal(err)
	}
	got, err := discoveryDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const want = "955f9a64d30839668e3de25f77eb8b48053bebbc48cf6b29df426796673cb80c"
	if got != want {
		t.Errorf("digest = %s, want %s", got, want)
	}
}

// TestDiscoveryDigestLeavesOutWhetherReposMayBeEmpty pins that a key deciding
// only an input's health does not move the digest, so setting it is not taken
// for a change to what is offered.
func TestDiscoveryDigestLeavesOutWhetherReposMayBeEmpty(t *testing.T) {
	digest := func(src string) string {
		t.Helper()
		cfg, err := config.Parse([]byte(src), "/settings/personal.yml")
		if err != nil {
			t.Fatal(err)
		}
		d, err := discoveryDigest(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	flagged := strings.Replace(digestFixture, `repos: ["/code/*"]`, `repos: ["/code/*"]`+"\n    repos_may_be_empty: true", 1)
	if flagged == digestFixture {
		t.Fatal("the fixture no longer holds the git input this spec flags")
	}
	if got, want := digest(flagged), digest(digestFixture); got != want {
		t.Errorf("digest = %s, want %s: repos_may_be_empty decides only health", got, want)
	}
}

// TestDiscoveryDigestFollowsWhetherASupplierIsWindowed pins the other side: a
// windowed supplier offers less, so setting it is a change to discovery and the
// first snapshot after it says so.
func TestDiscoveryDigestFollowsWhetherASupplierIsWindowed(t *testing.T) {
	windowed := strings.Replace(digestFixture, "max_age: 24h", "max_age: 24h\n    windowed: true", 1)
	if windowed == digestFixture {
		t.Fatal("the fixture no longer holds the supplied input this spec windows")
	}
	cfg, err := config.Parse([]byte(windowed), "/settings/personal.yml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := discoveryDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The digest TestDiscoveryDigestOfASettingWithoutAKeyPatternIsUnchanged pins.
	if d == "955f9a64d30839668e3de25f77eb8b48053bebbc48cf6b29df426796673cb80c" {
		t.Errorf("digest = %s, the unwindowed setting's: windowed changes what is offered", d)
	}
}

// TestDiscoveryDigestOfASettingWithAKeyPatternFollowsTheJoinRules pins that a
// change to what a key joins changes a patterned setting's digest, so the
// first snapshot after it reports discovery_changed and a reader can tell the
// work it newly places from activity, as with any change to discovery. The
// values are the digests each earlier version of the rules gave this setting:
// keys alone, then descriptions and pull requests.
func TestDiscoveryDigestOfASettingWithAKeyPatternFollowsTheJoinRules(t *testing.T) {
	cfg, err := config.Parse([]byte("key_pattern: '([A-Z]+-[0-9]+)'\n"+digestFixture), "/settings/personal.yml")
	if err != nil {
		t.Fatal(err)
	}
	got, err := discoveryDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, before := range []string{
		"c2026b62cbe66fa0f969d81564cd5f046e698686ac56eed760380be4a971ccb1",
		"976a61b7fd619ac8a99bfa11b5405daa1510883065b4cf2be4afdad99f81dfb2",
	} {
		if got == before {
			t.Errorf("digest = %s, the digest of earlier join rules", got)
		}
	}
}
