package git

import (
	"net/url"
	"slices"
	"strings"
)

// remote is where a repository's origin lives: a forge host and the path of
// the repository on it, both lowercase, since forges compare them without
// regard to case. The first segment of path is the owner.
type remote struct {
	host string
	path string
}

func (r remote) owner() string {
	owner, _, _ := strings.Cut(r.path, "/")
	return owner
}

// parseRemote reads a remote URL in git's URL or scp-like form, as
// git-clone(1)'s GIT URLS section describes them. A local path or file://
// remote names no forge, so it is not one; nor is a URL whose path has no
// owner and repository.
func parseRemote(raw string) (remote, bool) {
	var host, p string
	if scheme, _, ok := strings.Cut(raw, "://"); ok {
		u, err := url.Parse(raw)
		if err != nil || strings.EqualFold(scheme, "file") {
			return remote{}, false
		}
		host, p = u.Hostname(), u.Path
	} else {
		// git reads host:path as scp-like only when no slash comes before the
		// colon; otherwise it is a local path.
		colon := strings.Index(raw, ":")
		if colon < 0 || strings.Contains(raw[:colon], "/") {
			return remote{}, false
		}
		host, p = raw[:colon], raw[colon+1:]
		if _, h, ok := strings.Cut(host, "@"); ok {
			host = h
		}
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	segments := strings.Split(p, "/")
	if host == "" || len(segments) < 2 || slices.Contains(segments, "") {
		return remote{}, false
	}
	return remote{host: strings.ToLower(host), path: strings.ToLower(p)}, true
}
