package baseline

import (
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
