// Package awaiting reads a file an outside supplier writes in the
// schema/awaiting.v1.json contract: replies the user owes. It reports the
// supplier's facts and judges the input's health as a supplied file's is;
// whether an entry is still waiting is decided once, in internal/readout.
package awaiting

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jakewan/lookout/internal/config"
	"github.com/jakewan/lookout/internal/health"
	"github.com/jakewan/lookout/internal/readout"
	"github.com/jakewan/lookout/internal/supplied"
	"github.com/jakewan/lookout/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type document struct {
	supplied.Envelope
	Awaiting []entryFields `json:"awaiting"`
}

type entryFields struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Kind       string          `json:"kind"`
	AskedAt    time.Time       `json:"asked_at"`
	AnsweredAt *time.Time      `json:"answered_at"`
	Subject    string          `json:"subject"`
	Links      json.RawMessage `json:"links"`
	Attrs      json.RawMessage `json:"attrs"`
}

// Reader reads awaiting files against the compiled awaiting.v1 contract.
type Reader struct {
	schema *jsonschema.Schema
}

// NewReader compiles the embedded contract once, for every file a run reads.
func NewReader() (*Reader, error) {
	sch, err := schema.Compile("awaiting.v1")
	if err != nil {
		return nil, err
	}
	return &Reader{schema: sch}, nil
}

// Read judges the awaiting input in at now and returns the replies owed its
// file reports. An empty file is a quiet day, so the input expects no records.
// A cancelled ctx reads nothing; the caller reports the interruption.
func (rd *Reader) Read(ctx context.Context, in config.Input, now time.Time) readout.Reading {
	r := readout.Reading{Health: health.New(in.Name), Items: make([]readout.Item, 0), Awaiting: make([]readout.Awaiting, 0)}
	if ctx.Err() != nil {
		return r
	}
	var entries []readout.Awaiting
	h, ok := supplied.Judge(in, now, rd.schema, func(data []byte) (supplied.Decoded, error) {
		var doc document
		if err := json.Unmarshal(data, &doc); err != nil {
			return supplied.Decoded{}, err
		}
		if err := supplied.Encodable(doc.FetchedAt, &doc.AttemptedAt); err != nil {
			return supplied.Decoded{}, err
		}
		d := supplied.Decoded{Envelope: doc.Envelope, Count: len(doc.Awaiting)}
		entries = make([]readout.Awaiting, 0, len(doc.Awaiting))
		for _, e := range doc.Awaiting {
			if err := supplied.Encodable(e.AskedAt, e.AnsweredAt); err != nil {
				return supplied.Decoded{}, err
			}
			entries = append(entries, readout.Awaiting{ID: e.ID, Title: e.Title, Kind: e.Kind, AskedAt: e.AskedAt, AnsweredAt: e.AnsweredAt, Subject: e.Subject, Links: e.Links, Attrs: e.Attrs})
			d.IDs = append(d.IDs, e.ID)
			// An answer stamped ahead of this machine's clock would hide an ask
			// indefinitely, and an ask so stamped would outrank every other.
			d.Times = append(d.Times, e.AskedAt)
			if e.AnsweredAt != nil {
				d.Times = append(d.Times, *e.AnsweredAt)
			}
		}
		return d, nil
	})
	r.Health = h
	if ok {
		r.Awaiting = entries
	}
	return r
}
