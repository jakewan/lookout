package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/readout"
	"github.com/jakewan/lookout/internal/supplied"
)

// read writes one setting's readout to stdout. A readout whose inputs are
// unhealthy still succeeds: reporting that is its job.
func read(args []string, env environment, stdout io.Writer, diag *log.Logger) int {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "")
	setting := fs.String("setting", "", "")
	if err := fs.Parse(args); err != nil {
		diag.Printf("lookout read: %v\n%s", err, usage)
		return exitUsage
	}
	if fs.NArg() > 0 {
		diag.Printf("lookout read: unexpected argument %q\n%s", fs.Arg(0), usage)
		return exitUsage
	}
	if !*asJSON {
		diag.Printf("lookout read: --json is required; JSON is the only output so far\n%s", usage)
		return exitUsage
	}

	path, err := config.Locate(env.getenv, *setting)
	if err != nil {
		diag.Printf("lookout read: %v", err)
		return exitFailure
	}
	cfg, err := config.Load(path)
	if err != nil {
		diag.Printf("lookout read: %v", err)
		return exitFailure
	}

	now := env.now()
	readings := make(map[string]supplied.Reading, len(cfg.Inputs))
	for _, in := range cfg.Inputs {
		r, readErr := supplied.Read(in, now)
		if readErr != nil {
			diag.Printf("lookout read: %v", readErr)
			return exitFailure
		}
		readings[in.Name] = r
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(readout.Build(cfg, readings, now)); err != nil {
		diag.Printf("lookout read: writing the readout: %v", err)
		return exitFailure
	}
	return 0
}
