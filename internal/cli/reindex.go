package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"path/filepath"

	"github.com/klauspost/compress/zstd"

	"github.com/truvity/audit/catalogue"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store"
)

// Reindex rebuilds a profile's index from the archive.
//
// This command is what makes the index a projection rather than a second
// record. Everything a search answers with can be derived again from the
// objects, and the objects are the ones under an object lock that a signed
// digest chain accounts for. That is what lets the writer carry on when the
// index is unreachable, lets a deployment change the shape of the index without
// a migration of the trail, and lets an operator throw the database away.
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
	// catalogue would not repair it.
	Fields   func(ctx context.Context, r *record.Record) (index.Fields, error)
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
	if r.Fields == nil {
		return report, fmt.Errorf(
			"reindex: a catalogue is required, or the index would be rebuilt without " +
				"the data columns and a later run could not repair it")
	}

	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return report, err
	}
	defer decoder.Close()

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
		body, err := r.Store.Get(ctx, key)
		if err != nil {
			report.Unreadable = append(report.Unreadable, key)
			continue
		}
		plain, err := decoder.DecodeAll(body, nil)
		if err != nil {
			report.Unreadable = append(report.Unreadable, key)
			continue
		}
		report.Objects++

		// The line number is the row's address in the object, so it is counted
		// as the object has it, blank lines and all.
		for n, line := range strings.Split(strings.TrimRight(string(plain), "\n"), "\n") {
			if line == "" {
				continue
			}
			var copied record.Record
			if err := record.Unmarshal([]byte(line), &copied); err != nil {
				report.Unreadable = append(report.Unreadable, fmt.Sprintf("%s:%d", key, n+1))
				continue
			}
			fields, err := r.Fields(ctx, &copied)
			if err != nil {
				return report, fmt.Errorf("reindex: %s:%d: %w", key, n+1, err)
			}
			batch = append(batch, index.RowOf(&copied, index.ObjectAt{Key: key, Line: n + 1}, fields))
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

// objects lists the profile's objects over the range.
//
// The archive puts the tenant between the profile and the date, so a day is not
// a prefix and cannot be listed as one. The profile is listed once and the days
// are matched, which costs one pass rather than one pass per day.
func (r Reindex) objects(ctx context.Context) ([]string, error) {
	prefix := "profile=" + r.Profile + "/"
	entries, err := r.Store.List(ctx, prefix, "", 0)
	if err != nil {
		return nil, fmt.Errorf("reindex: listing %s: %w", prefix, err)
	}
	wanted := map[string]bool{}
	for day := r.From.UTC().Truncate(24 * time.Hour); !day.After(r.To.UTC()); day = day.AddDate(0, 0, 1) {
		wanted[day.Format("2006-01-02")] = true
	}

	var keys []string
	for _, e := range entries {
		if day, ok := dayOf(e.Key); ok && wanted[day] {
			keys = append(keys, e.Key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// dayOf reads the date out of an object key. The key is
// profile=<name>/tenant=<id>/year=YYYY/month=MM/day=DD/<object>, so the date is
// read by name rather than by position: a layout that gains a segment should
// not silently reindex the wrong day.
func dayOf(key string) (string, bool) {
	var year, month, day string
	for _, segment := range strings.Split(key, "/") {
		switch {
		case strings.HasPrefix(segment, "year="):
			year = strings.TrimPrefix(segment, "year=")
		case strings.HasPrefix(segment, "month="):
			month = strings.TrimPrefix(segment, "month=")
		case strings.HasPrefix(segment, "day="):
			day = strings.TrimPrefix(segment, "day=")
		}
	}
	if year == "" || month == "" || day == "" {
		return "", false
	}
	return year + "-" + month + "-" + day, true
}

func (r Reindex) batch() int {
	if r.Batch > 0 {
		return r.Batch
	}
	return 500
}

// CatalogueFields loads catalogues and returns the Fields a reindex needs.
//
// It resolves a record the way the writer did: by the source and version the
// record itself carries, never by whatever catalogue happens to be newest. A
// record written against version 1.2.0 is rebuilt against 1.2.0, so an index
// rebuilt today has the columns the record had when it was written.
func CatalogueFields(documents []string) (func(context.Context, *record.Record) (index.Fields, error), error) {
	catalogues := map[string]*catalogue.Catalogue{}
	for _, doc := range documents {
		c, err := catalogue.LoadFS(os.DirFS(filepath.Dir(doc)), filepath.Base(doc))
		if err != nil {
			return nil, fmt.Errorf("catalogue %s: %w", doc, err)
		}
		catalogues[c.Source+"@"+c.Version] = c
	}

	// One action's composed shape is the same for every record of it, and a
	// reindex walks millions, so the answer is worked out once.
	composed := map[string]index.Fields{}
	return func(_ context.Context, r *record.Record) (index.Fields, error) {
		key := r.GetSource() + "@" + r.GetCatalogueVersion()
		cached, ok := composed[key+"/"+r.GetAction()]
		if ok {
			return cached, nil
		}
		c, ok := catalogues[key]
		if !ok {
			return index.Fields{}, fmt.Errorf(
				"no catalogue %s version %s was given, and %s was written against it",
				r.GetSource(), r.GetCatalogueVersion(), r.GetAction())
		}
		x, err := c.Compose(r.GetAction())
		if err != nil {
			return index.Fields{}, fmt.Errorf("%s: %w", r.GetAction(), err)
		}
		fields := index.Fields{}
		if x.Data != nil {
			fields = index.Fields{Filter: x.Data.Filterable(), Facet: x.Data.Facets()}
		}
		composed[key+"/"+r.GetAction()] = fields
		return fields, nil
	}, nil
}
