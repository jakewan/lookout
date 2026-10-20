package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const editable = "inputs:\n  - name: tracker\n    supplied: tracker.json\n    max_age: 24h\nworkstreams:\n  - name: billing\n    members:\n      - tracker:ABC-1\n"

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
	if err := doc.Adopt(Member{Input: "tracker", ID: "ABC-2"}, "billing", false); err != nil {
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

func TestSaveKeepsTheFilesMode(t *testing.T) {
	doc, path := openEditable(t)
	if err := doc.Adopt(Member{Input: "tracker", ID: "ABC-2"}, "billing", false); err != nil {
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
	if err := viaLink.Adopt(Member{Input: "tracker", ID: "ABC-2"}, "billing", false); err != nil {
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
