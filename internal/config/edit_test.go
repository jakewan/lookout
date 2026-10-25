package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const editable = "inputs:\n  - name: tracker\n    kind: supplied\n    supplied: tracker.json\n    max_age: 24h\nworkstreams:\n  - name: billing\n    members:\n      - tracker:ABC-1\n"

func openEditable(t *testing.T) (*Document, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "personal.yml")
	if err := os.WriteFile(path, []byte(editable), 0o640); err != nil {
		t.Fatal(err)
	}
	doc, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return doc, path
}

func contents(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSaveRefusesWhenTheFileChangedUnderneath(t *testing.T) {
	doc, path := openEditable(t)
	if _, err := doc.Adopt(Member{Input: "tracker", ID: "ABC-2"}, "billing", false); err != nil {
		t.Fatal(err)
	}
	edited := editable + "# saved from an editor meanwhile\n"
	beforeRename = func() {
		if err := os.WriteFile(path, []byte(edited), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { beforeRename = func() {} })

	err := doc.Save()

	if _, ok := errors.AsType[*Refusal](err); !ok {
		t.Fatalf("Save = %v, want a refusal", err)
	}
	if got := contents(t, path); got != edited {
		t.Errorf("file = %q, want the other writer's save kept", got)
	}
	leftovers, globErr := filepath.Glob(filepath.Join(filepath.Dir(path), ".*tmp*"))
	if globErr != nil || len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v %v", leftovers, globErr)
	}
}

// TestSaveNamesItsTemporaryFileSoItIsNeverASetting pins the name a crash would
// leave behind: one Locate's search for setting files skips.
func TestSaveNamesItsTemporaryFileSoItIsNeverASetting(t *testing.T) {
	doc, path := openEditable(t)
	if _, err := doc.Adopt(Member{Input: "tracker", ID: "ABC-2"}, "billing", false); err != nil {
		t.Fatal(err)
	}
	var during []string
	beforeRename = func() {
		var err error
		if during, err = settingFiles(filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { beforeRename = func() {} })

	if err := doc.Save(); err != nil {
		t.Fatal(err)
	}
	if len(during) != 1 || during[0] != path {
		t.Errorf("setting files while the temporary file exists = %v, want only %s", during, path)
	}
}

func TestSaveKeepsTheFilesMode(t *testing.T) {
	doc, path := openEditable(t)
	if _, err := doc.Adopt(Member{Input: "tracker", ID: "ABC-2"}, "billing", false); err != nil {
		t.Fatal(err)
	}
	if err := doc.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", info.Mode().Perm())
	}
}

func TestSaveReplacesASymlinksTarget(t *testing.T) {
	_, target := openEditable(t)
	link := filepath.Join(t.TempDir(), "personal.yml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	viaLink, err := Open(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := viaLink.Adopt(Member{Input: "tracker", ID: "ABC-2"}, "billing", false); err != nil {
		t.Fatal(err)
	}
	if err := viaLink.Save(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced by a file: %v %v", info, err)
	}
	if got := contents(t, target); got == editable {
		t.Errorf("target unchanged, want the edit written through the link")
	}
}

func TestEveryEditKeepsExplicitOnly(t *testing.T) {
	const workstreams = "workstreams:\n  - name: billing\n    members:\n      - tracker:ABC-1\n  - name: onboarding\n    members:\n      - tracker:ABC-9\n"
	const inputs = "inputs:\n  - name: tracker\n    kind: supplied\n    supplied: tracker.json\n    max_age: 24h\n"
	const key = "explicit_only: true\n"
	edits := map[string]func(*Document) error{
		"adopt": func(d *Document) error {
			_, err := d.Adopt(Member{Input: "tracker", ID: "ABC-3"}, "billing", false)
			return err
		},
		"adopt into a new workstream": func(d *Document) error {
			_, err := d.Adopt(Member{Input: "tracker", ID: "ABC-3"}, "payments", true)
			return err
		},
		"move": func(d *Document) error {
			_, err := d.Move(Member{Input: "tracker", ID: "ABC-1"}, "onboarding", false)
			return err
		},
		"remove": func(d *Document) error {
			_, err := d.Remove(Member{Input: "tracker", ID: "ABC-9"})
			return err
		},
		"rename":   func(d *Document) error { return d.Rename("billing", "payments") },
		"order":    func(d *Document) error { return d.Order([]string{"onboarding"}) },
		"complete": func(d *Document) error { return d.Complete("billing", "2030-06-15") },
	}
	for place, text := range map[string]string{
		"before the workstreams": key + inputs + workstreams,
		"after the workstreams":  inputs + workstreams + key,
	} {
		for name, change := range edits {
			t.Run(place+"/"+name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "personal.yml")
				if err := os.WriteFile(path, []byte(text), 0o640); err != nil {
					t.Fatal(err)
				}
				doc, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				if changeErr := change(doc); changeErr != nil {
					t.Fatal(changeErr)
				}
				if saveErr := doc.Save(); saveErr != nil {
					t.Fatal(saveErr)
				}
				got := contents(t, path)
				if !strings.Contains(got, "\n"+key) && !strings.HasPrefix(got, key) {
					t.Errorf("file = %q, want the explicit_only line kept", got)
				}
				cfg, err := Load(path)
				if err != nil {
					t.Fatal(err)
				}
				if !cfg.ExplicitOnly {
					t.Errorf("ExplicitOnly = false after %s", name)
				}
			})
		}
	}
}

func TestApplyRejectsAnEditThatDropsExplicitOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "personal.yml")
	if err := os.WriteFile(path, []byte("explicit_only: true\n"+editable), 0o640); err != nil {
		t.Fatal(err)
	}
	doc, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.apply([]byte(editable), doc.cfg.clone()); err == nil {
		t.Error("apply accepted an edit that dropped explicit_only")
	}
}

func TestApplyRejectsAnEditThatParsesWrong(t *testing.T) {
	cases := map[string]string{
		"invalid":                "inputs: [\n",
		"more than was intended": editable + "      - tracker:ABC-7\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			doc, path := openEditable(t)
			want := doc.cfg.clone()
			want.Workstreams[0].Members = append(want.Workstreams[0].Members, Member{Input: "tracker", ID: "ABC-2"})

			if err := doc.apply([]byte(text), want); err == nil {
				t.Fatal("apply accepted it")
			}
			if err := doc.Save(); err != nil {
				t.Fatal(err)
			}
			if got := contents(t, path); got != editable {
				t.Errorf("file = %q, want it untouched", got)
			}
		})
	}
}
