// Package observe is the third part of an installation: it turns the archive
// into the search index by following the bucket (docs/decisions/0020).
//
// The listing is the source of truth. A cursor per profile and tenant says how
// far the keys have been read, a settle window keeps the cursor behind anything
// a writer could still be putting, and a notification only makes the next pass
// come sooner. The index it writes is a projection: dropping the cursors and
// running again rebuilds it from the bucket alone.
package observe

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/internal/recobj"
	"github.com/truvity/audit/sdk/catalogue"
	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/store"
)

// Fields says which of an action's extension properties are indexed. It is the
// catalogue's answer, resolved the way the writer resolved it. An index built
// without it would be missing its data columns, and because indexing is
// idempotent a later run with the catalogue would not repair it, so a record
// whose catalogue cannot be found is an error and never an empty answer.
type Fields func(ctx context.Context, r *record.Record) (index.Fields, error)

// Catalogues finds the catalogue a record was written against.
type Catalogues interface {
	Get(ctx context.Context, source, version string) (*catalogue.Catalogue, error)
}

// FieldsFrom resolves a record by the source and version it carries, never by
// whatever catalogue happens to be newest: a record written against 1.2.0 is
// indexed against 1.2.0, so an index rebuilt today has the columns the record
// had when it was written.
func FieldsFrom(from Catalogues) Fields {
	// One action's composed shape is the same for every record of it, and an
	// index is built from millions, so the answer is worked out once.
	var mu sync.Mutex
	composed := map[string]index.Fields{}
	return func(ctx context.Context, r *record.Record) (index.Fields, error) {
		key := r.GetSource() + "@" + r.GetCatalogueVersion() + "/" + r.GetAction()
		mu.Lock()
		cached, ok := composed[key]
		mu.Unlock()
		if ok {
			return cached, nil
		}
		c, err := from.Get(ctx, r.GetSource(), r.GetCatalogueVersion())
		if err != nil {
			return index.Fields{}, fmt.Errorf(
				"no catalogue %s version %s, and %s was written against it: %w",
				r.GetSource(), r.GetCatalogueVersion(), r.GetAction(), err)
		}
		x, err := c.Compose(r.GetAction())
		if err != nil {
			return index.Fields{}, fmt.Errorf("%s: %w", r.GetAction(), err)
		}
		fields := index.Fields{}
		if x.Data != nil {
			fields = index.Fields{Filter: x.Data.Filterable(), Facet: x.Data.Facets()}
		}
		mu.Lock()
		composed[key] = fields
		mu.Unlock()
		return fields, nil
	}
}

// ErrNoCatalogue is returned for a catalogue that is in no place looked.
var ErrNoCatalogue = errors.New("observe: no such catalogue")

// Given are catalogues an operator named, by source and version.
type Given map[string]*catalogue.Catalogue

// Get implements Catalogues.
func (g Given) Get(_ context.Context, source, version string) (*catalogue.Catalogue, error) {
	if c, ok := g[source+"@"+version]; ok {
		return c, nil
	}
	return nil, ErrNoCatalogue
}

// With adds catalogues to a set.
func (g Given) With(cs ...*catalogue.Catalogue) Given {
	for _, c := range cs {
		g[c.Source+"@"+c.Version] = c
	}
	return g
}

// ArchiveCatalogues reads catalogues from where the writer put them: the
// archive's own catalogue/<app>/<version> and the extension schemas beside it
// (docs/reference/bucket-contract.md). Observe therefore needs no copy of the
// catalogues and no registry: what describes a record is in the bucket that
// holds it, from before the first record that names it.
type ArchiveCatalogues struct {
	Store store.Store

	mu    sync.Mutex
	known map[string]*catalogue.Catalogue
}

// Get implements Catalogues.
func (a *ArchiveCatalogues) Get(ctx context.Context, source, version string) (*catalogue.Catalogue, error) {
	for what, part := range map[string]string{"source": source, "version": version} {
		if why := store.KeyComponent(part); why != "" {
			return nil, fmt.Errorf("observe: the catalogue's %s %q %s", what, part, why)
		}
	}
	id := source + "@" + version
	a.mu.Lock()
	c, ok := a.known[id]
	a.mu.Unlock()
	if ok {
		return c, nil
	}

	document, err := a.Store.Get(ctx, store.CatalogueKey(source, version))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNoCatalogue
	}
	if err != nil {
		return nil, fmt.Errorf("observe: reading catalogue %s: %w", id, err)
	}
	dir := store.SchemaDir(source, version)
	entries, err := a.Store.List(ctx, dir, "", 1000)
	if err != nil {
		return nil, fmt.Errorf("observe: listing %s: %w", dir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	schemas := make([][]byte, 0, len(entries))
	for _, e := range entries {
		raw, err := a.Store.Get(ctx, e.Key)
		if err != nil {
			return nil, fmt.Errorf("observe: reading %s: %w", e.Key, err)
		}
		schemas = append(schemas, raw)
	}
	c, err = catalogue.Load(document, schemas)
	if err != nil {
		return nil, fmt.Errorf("observe: catalogue %s: %w", id, err)
	}
	a.mu.Lock()
	if a.known == nil {
		a.known = map[string]*catalogue.Catalogue{}
	}
	a.known[id] = c
	a.mu.Unlock()
	return c, nil
}

// Chain tries each in turn and takes the first that has the catalogue. An error
// other than not having it stops the search: a catalogue that is there and
// cannot be read is not one to quietly replace with another.
type Chain []Catalogues

// Get implements Catalogues.
func (c Chain) Get(ctx context.Context, source, version string) (*catalogue.Catalogue, error) {
	for _, from := range c {
		found, err := from.Get(ctx, source, version)
		if err == nil {
			return found, nil
		}
		if !errors.Is(err, ErrNoCatalogue) {
			return nil, err
		}
	}
	return nil, ErrNoCatalogue
}

// ErrFetch marks an object that could not be fetched. It is the one failure of
// reading an object that is about the store and not about the object: asking
// again may succeed, where an object that does not decode never will.
var ErrFetch = errors.New("observe: the object could not be fetched")

// ReadObject turns one record object into the rows the index holds for it.
//
// It is the one place an object becomes rows, shared by the cursor indexer and
// by `audit reindex`, so that a rebuild over a range and the following of the
// bucket cannot disagree. Lines are numbered from one, as a person counts them.
//
// An object that is gone (a lifecycle rule took it) yields nothing. An object
// that cannot be decoded, and a line that cannot, are reported in unreadable
// and skipped: they never will decode, and an index that waited for them would
// wait for ever. A fetch that failed, and a record whose catalogue cannot be
// found, are errors, because both can be fixed and then the object read.
func ReadObject(ctx context.Context, s store.Store, key string, fields Fields) (rows []index.Row, unreadable []string, err error) {
	body, err := s.Get(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, nil, nil
	case err != nil:
		return nil, nil, fmt.Errorf("%w: %s: %w", ErrFetch, key, err)
	}
	lines, err := recobj.Decode(body)
	if err != nil {
		return nil, []string{key}, nil
	}
	for n, line := range lines {
		copied, err := line.Decoded()
		if err != nil {
			unreadable = append(unreadable, fmt.Sprintf("%s:%d", key, n+1))
			continue
		}
		f, err := fields(ctx, copied)
		if err != nil {
			return nil, nil, fmt.Errorf("%s:%d: %w", key, n+1, err)
		}
		rows = append(rows, index.RowOf(copied, index.ObjectAt{Key: key, Line: n + 1}, f))
	}
	return rows, unreadable, nil
}
