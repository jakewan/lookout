// Package schema embeds lookout's published contracts, so the files a supplier
// or renderer validates against are the same bytes lookout enforces.
package schema

import (
	"bytes"
	"embed"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed *.json
var files embed.FS

const base = "https://raw.githubusercontent.com/jakewan/lookout/main/schema/"

// Compile returns the named contract, such as "supplied.v1", ready to validate
// a decoded JSON document. Every contract is registered first, so one may refer
// to another by its $id.
func Compile(name string) (*jsonschema.Schema, error) {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("listing embedded schemas: %w", err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	for _, e := range entries {
		data, readErr := files.ReadFile(e.Name())
		if readErr != nil {
			return nil, fmt.Errorf("reading embedded schema %s: %w", e.Name(), readErr)
		}
		doc, decodeErr := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if decodeErr != nil {
			return nil, fmt.Errorf("decoding embedded schema %s: %w", e.Name(), decodeErr)
		}
		if addErr := c.AddResource(base+e.Name(), doc); addErr != nil {
			return nil, fmt.Errorf("registering schema %s: %w", e.Name(), addErr)
		}
	}
	sch, err := c.Compile(base + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("compiling schema %s: %w", name, err)
	}
	return sch, nil
}
