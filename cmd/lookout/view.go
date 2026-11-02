package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/dirlock"
	"github.com/jakewan/lookout/internal/htmlview"
	"github.com/jakewan/lookout/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// view writes one setting's readout as a page a browser opens, and prints the
// page's path. Like show, it draws only from the readout's published JSON and
// succeeds over unhealthy inputs. With --readout it renders a readout saved
// earlier, or read from stdin, without reading any configuration.
func view(ctx context.Context, args []string, env environment, stdout io.Writer, diag *log.Logger) int {
	fs, setting := newFlags("view")
	from := fs.String("readout", "", "")
	pos, ok := parseFlags(fs, setting, args, diag)
	if !ok {
		return exitUsage
	}
	readoutGiven := false
	fs.Visit(func(f *flag.Flag) { readoutGiven = readoutGiven || f.Name == "readout" })
	switch {
	case len(pos) > 0:
		diag.Printf("lookout view: unexpected argument %q\n%s", pos[0], usage)
		return exitUsage
	case readoutGiven && *from == "":
		diag.Printf("lookout view: --readout needs a file, or - for stdin\n%s", usage)
		return exitUsage
	case readoutGiven && *setting != "":
		// A saved readout names its own setting; a second name could only
		// disagree with it.
		diag.Printf("lookout view: --readout and --setting cannot be combined\n%s", usage)
		return exitUsage
	}
	var data []byte
	var err error
	if readoutGiven {
		data, err = savedReadout(*from, env.stdin)
	} else {
		data, err = liveReadout(ctx, env, *setting)
	}
	if err == nil {
		var path string
		path, err = viewPage(env.getenv, data)
		if err == nil {
			_, err = fmt.Fprintln(stdout, path)
		}
	}
	if err != nil {
		diag.Printf("lookout view: %v", err)
		return exitFailure
	}
	return 0
}

// viewPage renders data, a readout.v1 document, and writes the page under the
// directory its setting names, returning the page's path.
func viewPage(getenv func(string) string, data []byte) (string, error) {
	var head struct {
		Setting string `json:"setting"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return "", fmt.Errorf("reading the readout: %w", err)
	}
	if nameErr := config.CheckName(head.Setting); nameErr != nil {
		return "", fmt.Errorf("the readout's %w", nameErr)
	}
	var page bytes.Buffer
	if renderErr := htmlview.Render(&page, data); renderErr != nil {
		return "", renderErr
	}
	return writeView(getenv, head.Setting, page.Bytes(), data)
}

func liveReadout(ctx context.Context, env environment, setting string) ([]byte, error) {
	out, err := currentReadout(ctx, env, setting)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encoding the readout: %w", err)
	}
	return data, nil
}

// savedReadout reads a readout from a file, or from stdin where name is -, and
// holds it to readout.v1. It comes from outside this invocation, so the
// contract is checked here rather than trusted as a readout lookout just built.
func savedReadout(name string, stdin io.Reader) ([]byte, error) {
	var data []byte
	var err error
	if name == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(name)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the readout: %w", err)
	}
	sch, err := schema.Compile("readout.v1")
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("reading the readout: %w", err)
	}
	if err := sch.Validate(doc); err != nil {
		return nil, fmt.Errorf("the readout does not meet readout.v1: %w", err)
	}
	return data, nil
}

// writeView writes the page, and the readout it was drawn from as view.json
// beside it, under the cache directory, and returns the page's path. Both are
// derived from a readout and safe to lose, so they are cache rather than
// state. They hold work data, so they are kept to their owner, and the modes
// are set explicitly because creating a path never tightens one that exists.
//
// Each is written beside its final name and renamed into place, so a browser
// or an agent reading one never reads half of it. Both are written under an
// exclusive lock on the directory, so two views written at once cannot leave
// one's page beside the other's readout; where no such lock exists, off Unix
// or on a filesystem without flock, they are written without it. The page
// names the read it shows exactly, so a reader of either can still tell the
// two apart.
func writeView(getenv func(string) string, setting string, page, readout []byte) (path string, err error) {
	base, err := cacheRoot(getenv)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, setting)
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return "", fmt.Errorf("creating the page's directory: %w", mkErr)
	}
	if chErr := os.Chmod(dir, 0o700); chErr != nil {
		return "", fmt.Errorf("restricting the page's directory: %w", chErr)
	}
	lock, err := dirlock.Lock(dir, "the page's directory", true)
	switch {
	case errors.Is(err, dirlock.ErrUnsupported):
	case err != nil:
		return "", err
	default:
		defer func() { err = errors.Join(err, lock.Close()) }()
	}
	if err = replace(dir, "view.json", readout); err != nil {
		return "", err
	}
	if err = replace(dir, "view.html", page); err != nil {
		return "", err
	}
	return filepath.Join(dir, "view.html"), nil
}

// replace writes data to name in dir, readable only by its owner, beside its
// final name and then renamed into place.
func replace(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	_, writeErr := tmp.Write(data)
	chmodErr := tmp.Chmod(0o600)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, chmodErr, closeErr); err != nil {
		return errors.Join(fmt.Errorf("writing %s: %w", name, err), os.Remove(tmp.Name()))
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return errors.Join(fmt.Errorf("writing %s: %w", name, err), os.Remove(tmp.Name()))
	}
	return nil
}

// cacheRoot is lookout's directory under the XDG cache base, resolved from
// the invocation's own environment as the state directory is.
func cacheRoot(getenv func(string) string) (string, error) {
	if base := getenv("XDG_CACHE_HOME"); base != "" {
		return filepath.Join(base, "lookout"), nil
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".cache", "lookout"), nil
	}
	return "", errors.New("cannot find the cache directory: neither XDG_CACHE_HOME nor HOME is set")
}
