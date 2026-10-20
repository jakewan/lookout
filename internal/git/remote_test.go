package git

import "testing"

func TestParseRemote(t *testing.T) {
	cases := []struct {
		url  string
		want remote
		ok   bool
	}{
		{url: "git@github.com:alice/app.git", want: remote{"github.com", "alice/app"}, ok: true},
		{url: "github.com:alice/app", want: remote{"github.com", "alice/app"}, ok: true},
		{url: "work-alias:Alice/App.git", want: remote{"work-alias", "alice/app"}, ok: true},
		{url: "https://github.com/alice/app.git", want: remote{"github.com", "alice/app"}, ok: true},
		{url: "https://alice:token@github.com/alice/app/", want: remote{"github.com", "alice/app"}, ok: true},
		// The user before the host is a login, never the owner.
		{url: "ssh://bob@git.example.com:2222/alice/app.git", want: remote{"git.example.com", "alice/app"}, ok: true},
		{url: "git://git.example.com/alice/app", want: remote{"git.example.com", "alice/app"}, ok: true},
		{url: "https://gitlab.example/group/sub/repo.git", want: remote{"gitlab.example", "group/sub/repo"}, ok: true},
		// Azure DevOps puts a version segment first, so its owner never matches
		// a forge_owners entry and the repository stays fenced out.
		{url: "git@ssh.dev.azure.com:v3/org/project/repo", want: remote{"ssh.dev.azure.com", "v3/org/project/repo"}, ok: true},
		{url: "file:///srv/mirrors/alice/app.git"},
		{url: "/mirrors/alice/app.git"},
		{url: "../alice/app.git"},
		{url: "./mirrors:alice/app"},
		{url: "https://github.com/alice"},
		{url: "git@github.com:alice"},
		{url: "https://github.com/alice//app"},
	}
	for _, tc := range cases {
		got, ok := parseRemote(tc.url)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseRemote(%q) = %+v, %v; want %+v, %v", tc.url, got, ok, tc.want, tc.ok)
		}
	}
}
