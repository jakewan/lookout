package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
)

// read writes one setting's readout to stdout. A readout whose inputs are
// unhealthy still succeeds: reporting that is its job.
func read(ctx context.Context, args []string, env environment, stdout io.Writer, diag *log.Logger) int {
	fs, setting := newFlags("read")
	asJSON := fs.Bool("json", false, "")
	pos, ok := parseFlags(fs, setting, args, diag)
	if !ok {
		return exitUsage
	}
	if len(pos) > 0 {
		diag.Printf("lookout read: unexpected argument %q\n%s", pos[0], usage)
		return exitUsage
	}
	if !*asJSON {
		diag.Printf("lookout read: --json is required; JSON is the only output so far\n%s", usage)
		return exitUsage
	}

	out, err := currentReadout(ctx, env, *setting)
	if err != nil {
		diag.Printf("lookout read: %v", err)
		return exitFailure
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		diag.Printf("lookout read: writing the readout: %v", err)
		return exitFailure
	}
	return 0
}

// unplaced lists the readout's unplaced items one per line, each starting with
// the token adopt and dismiss take, so a person can act on one without reading
// JSON.
func unplaced(ctx context.Context, args []string, env environment, stdout io.Writer, diag *log.Logger) int {
	fs, setting := newFlags("unplaced")
	pos, ok := parseFlags(fs, setting, args, diag)
	if !ok {
		return exitUsage
	}
	if len(pos) > 0 {
		diag.Printf("lookout unplaced: unexpected argument %q\n%s", pos[0], usage)
		return exitUsage
	}
	out, err := currentReadout(ctx, env, *setting)
	if err != nil {
		diag.Printf("lookout unplaced: %v", err)
		return exitFailure
	}
	// Tabs and line breaks in a title would split its line.
	flat := strings.NewReplacer("\t", " ", "\r", " ", "\n", " ")
	for _, p := range out.Unplaced {
		var it struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			LastTouched string `json:"last_touched"`
		}
		if err := json.Unmarshal(p.Item, &it); err != nil {
			diag.Printf("lookout unplaced: reading an item of %s: %v", p.Input, err)
			return exitFailure
		}
		token := p.Input + ":" + it.ID
		if strings.ContainsAny(token, "\t\r\n") {
			token = strconv.Quote(token)
		}
		if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\n", token, flat.Replace(it.Title), it.LastTouched); err != nil {
			diag.Printf("lookout unplaced: %v", err)
			return exitFailure
		}
	}
	return 0
}
