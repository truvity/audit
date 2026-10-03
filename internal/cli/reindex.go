package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"path/filepath"

	"github.com/truvity/audit/sdk/catalogue"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/internal/observe"
	"github.com/truvity/audit/store"
)

// Reindex rebuilds a profile's index from the archive.
//
// This command is what makes the index a projection rather than a second
// record. Everything a search answers with can be derived again from the
// objects, and the objects are the ones under an object lock that carry their
// own hashes. That is what lets the writer carry on when the
// index is unreachable, lets a deployment change the shape of the index without
// a migration of the trail, and lets an operator throw the database away.
//
// It is a batch over a range of ingest days, for repairing one without waiting
// for the cursor to come round, and it shares what turns an object into rows
// with the indexer that follows the bucket (internal/observe): the two cannot
// disagree about what an object holds. The other repair is to reset the cursor
// (Reset) and let that indexer catch up.
//
// It is safe to run over a range that is already indexed: an index counts a
// record once, and the line a row points at is the line the object has, so a
// second pass over the same objects changes nothing.
type Reindex struct {
	Store store.Store
	Index index.Indexer
	// Fields says which of an action's extension properties are indexed. It is
	// the catalogue's answer, resolved the same way the writer resolved it. A
	// reindex without it would quietly produce an index missing its data
	// columns, and because indexing is idempotent, a later run with the
	// catalogue would not repair it. Unset, the catalogues are read from the
	// archive, where the writer put each before any record that names it.
	Fields   observe.Fields
	Profile  string
	From, To time.Time
	// Batch is how many rows are indexed at a time. Default 500.
	Batch int
	JSON  bool
	Out   io.Writer
}

// ReindexReport is what a reindex read and wrote.
type ReindexReport struct {
	Profile string `json:"profile"`
	Objects int    `json:"objects"`
	Records int    `json:"records"`
	// Unreadable counts objects that could not be read or decoded. They are
	// reported rather than skipped in silence: an object the archive holds and
	// nothing can read is a finding, not a detail.
	Unreadable []string `json:"unreadable,omitempty"`
}

// String is the human form.
func (r ReindexReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "profile %s: %d records from %d objects\n", r.Profile, r.Records, r.Objects)
	for _, key := range r.Unreadable {
		fmt.Fprintf(&b, "  unreadable: %s\n", key)
	}
	return b.String()
}

// Run walks the range and indexes what it finds.
func (r Reindex) Run(ctx context.Context) (ReindexReport, error) {
	out := r.Out
	if out == nil {
		out = os.Stdout
	}
	report := ReindexReport{Profile: r.Profile}
	fields := r.Fields
	if fields == nil {
		fields = observe.FieldsFrom(&observe.ArchiveCatalogues{Store: r.Store})
	}

	keys, err := r.objects(ctx)
	if err != nil {
		return report, err
	}

	batch := make([]index.Row, 0, r.batch())
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := r.Index.Index(ctx, r.Profile, batch); err != nil {
			return fmt.Errorf("reindex: %w", err)
		}
		batch = batch[:0]
		return nil
	}

	for _, key := range keys {
		rows, unreadable, err := observe.ReadObject(ctx, r.Store, key, fields)
		report.Unreadable = append(report.Unreadable, unreadable...)
		switch {
		case errors.Is(err, observe.ErrFetch):
			report.Unreadable = append(report.Unreadable, key)
			continue
		case err != nil:
			return report, fmt.Errorf("reindex: %w", err)
		}
		if len(unreadable) == 1 && unreadable[0] == key {
			continue
		}
		report.Objects++
		for _, row := range rows {
			batch = append(batch, row)
			report.Records++
			if len(batch) >= r.batch() {
				if err := flush(); err != nil {
					return report, err
				}
			}
		}
	}
	if err := flush(); err != nil {
		return report, err
	}

	if r.JSON {
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return report, err
		}
		printf(out, "%s\n", body)
	} else {
		printf(out, "%s", report.String())
	}
	return report, nil
}

// objects lists the profile's objects over the range of days, tenant by tenant
// and in key order, which is ingest order: the shape the archive has, and the
// only one that stays bounded as it grows. The range is of INGEST time, which
// is what the keys name, and both its days are included.
func (r Reindex) objects(ctx context.Context) ([]string, error) {
	var keys []string
	from := r.From.UTC().Truncate(24 * time.Hour)
	to := r.To.UTC().Truncate(24 * time.Hour).Add(23 * time.Hour)
	err := store.WalkProfile(ctx, r.Store, r.Profile, from, to, func(_ string, e store.Entry) error {
		keys = append(keys, e.Key)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reindex: listing profile %s: %w", r.Profile, err)
	}
	return keys, nil
}

func (r Reindex) batch() int {
	if r.Batch > 0 {
		return r.Batch
	}
	return 500
}

// CatalogueFields loads catalogue documents and returns the Fields a reindex
// needs, falling back to the archive's own copy of a catalogue the files do not
// include. See observe.FieldsFrom for how a record is resolved.
func CatalogueFields(documents []string, from store.Store) (observe.Fields, error) {
	given := observe.Given{}
	for _, doc := range documents {
		c, err := catalogue.LoadFS(os.DirFS(filepath.Dir(doc)), filepath.Base(doc))
		if err != nil {
			return nil, fmt.Errorf("catalogue %s: %w", doc, err)
		}
		given.With(c)
	}
	chain := observe.Chain{given}
	if from != nil {
		chain = append(chain, &observe.ArchiveCatalogues{Store: from})
	}
	return observe.FieldsFrom(chain), nil
}

// Reset forgets where the indexer had read a profile to (or one tenant of it),
// so that it reads that prefix again from the start and catches up. The index
// keeps what it has: a row that is there is left alone.
func Reset(ctx context.Context, cursors observe.Cursors, profile, tenant string) error {
	return cursors.ResetCursor(ctx, profile, tenant)
}
