// Command npmregistry is a development tool that fails when package-lock.json
// would install a package from anywhere but the public npm registry. It is not
// part of the lookout binary.
//
// npm writes into the lockfile whichever registry the installing contributor's
// own configuration names, so a lockfile written behind a private mirror would
// publish that mirror's address and send every later install to it. The
// project's .npmrc names the public registry; this check is what holds the
// committed lockfile to it.
package main

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"slices"
	"strings"
)

// registry is the only source a locked package may come from. The trailing
// slash keeps a host that merely begins with this name from passing.
const registry = "https://registry.npmjs.org/"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// lock is the part of an npm lockfile, version 2 or later, the check reads.
type lock struct {
	Packages map[string]struct {
		Resolved  string `json:"resolved"`
		Integrity string `json:"integrity"`
		Link      bool   `json:"link"`
	} `json:"packages"`
}

func run(args []string, stderr io.Writer) int {
	diag := log.New(stderr, "npmregistry: ", 0)
	path := "package-lock.json"
	if len(args) > 0 {
		path = args[0]
	}
	data, err := os.ReadFile(path)
	if err != nil {
		diag.Printf("%v", err)
		return 1
	}
	var l lock
	if jsonErr := json.Unmarshal(data, &l); jsonErr != nil {
		diag.Printf("reading %s: %v", path, jsonErr)
		return 1
	}
	if l.Packages == nil {
		diag.Printf("%s has no packages map; regenerate it with a current npm", path)
		return 1
	}
	var refused []string
	for name, p := range l.Packages {
		// The empty key is the project itself, which is not installed from anywhere.
		if name == "" {
			continue
		}
		if p.Link || !strings.HasPrefix(p.Resolved, registry) || p.Integrity == "" {
			refused = append(refused, name)
		}
	}
	if len(refused) == 0 {
		return 0
	}
	slices.Sort(refused)
	for _, name := range refused {
		diag.Printf("%s is not locked to a package with an integrity hash at %s", name, registry)
	}
	diag.Printf("reinstall with the project's .npmrc in effect (npm install from the repository root), then commit package-lock.json")
	return 1
}
