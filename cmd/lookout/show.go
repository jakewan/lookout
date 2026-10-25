package main

import (
	"context"
	"encoding/json"
	"io"
	"log"

	"github.com/jakewan/lookout/internal/textview"
)

// show writes one setting's readout as a plain-text view for a person. The
// view is drawn from the readout's published JSON, not from lookout's own
// types, so it shows only what any renderer could. Like read, it succeeds over
// unhealthy inputs: showing them is its job.
func show(ctx context.Context, args []string, env environment, stdout io.Writer, diag *log.Logger) int {
	fs, setting := newFlags("show")
	pos, ok := parseFlags(fs, setting, args, diag)
	if !ok {
		return exitUsage
	}
	if len(pos) > 0 {
		diag.Printf("lookout show: unexpected argument %q\n%s", pos[0], usage)
		return exitUsage
	}
	out, err := currentReadout(ctx, env, *setting)
	if err != nil {
		diag.Printf("lookout show: %v", err)
		return exitFailure
	}
	data, err := json.Marshal(out)
	if err != nil {
		diag.Printf("lookout show: encoding the readout: %v", err)
		return exitFailure
	}
	if err := textview.Render(stdout, data); err != nil {
		diag.Printf("lookout show: %v", err)
		return exitFailure
	}
	return 0
}
