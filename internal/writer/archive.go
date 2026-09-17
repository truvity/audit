package writer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sync"
	"time"

	"github.com/truvity/audit"
	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/internal/schemagen"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store"
)

// SchemaArchive copies into the archive whatever is needed to read it.
//
// A locked object outlives the deployment that wrote it, and very likely this
// repository. What sits beside it therefore has to be enough on its own: the
// catalogue that describes the actions, the extension schemas that describe
// their data, the record's own JSON Schema with the proto's comments carried
// as descriptions, and the proto itself for what a schema cannot say.
//
// Copies are made on first use and never again. A catalogue version is
// immutable by construction — a changed catalogue is a new version — so a key
// already taken is the right answer and not a collision.
type SchemaArchive struct {
	Store store.Store
	// RetainUntil is how long a schema is kept. It must be at least as long as
	// the longest-lived record that names it: a record whose schema has been
	// deleted is a record nobody can read, which is the one thing the archive
	// exists to prevent.
	RetainUntil func(at time.Time) time.Time
	Now         func() time.Time

	done sync.Map
}

// SchemaPrefix is where the things that describe records live, away from the
// records themselves so that a policy can keep them for longer.
const SchemaPrefix = "schema"

// EnsureCatalogue copies a catalogue version and its extension schemas.
func (a *SchemaArchive) EnsureCatalogue(ctx context.Context, c *catalogue.Catalogue) error {
	mark := "catalogue/" + c.Source + "@" + c.Version
	if _, seen := a.done.Load(mark); seen {
		return nil
	}
	base := fmt.Sprintf("%s/%s/%s", SchemaPrefix, c.Source, c.Version)
	if err := a.put(ctx, base+"/catalogue.yaml", c.Document(), "application/yaml"); err != nil {
		return err
	}
	for id, raw := range c.Schemas() {
		if err := a.put(ctx, base+"/"+schemaFileName(id), raw, "application/schema+json"); err != nil {
			return err
		}
	}
	a.done.Store(mark, true)
	return nil
}

// EnsureRecord copies the record's own schema and proto for a major version.
func (a *SchemaArchive) EnsureRecord(ctx context.Context, schemaVersion string) error {
	major, _, err := record.ParseSchemaVersion(schemaVersion)
	if err != nil {
		return err
	}
	mark := fmt.Sprintf("record/v%d", major)
	if _, seen := a.done.Load(mark); seen {
		return nil
	}
	base := fmt.Sprintf("%s/audit/v%d", SchemaPrefix, major)

	published, err := schemagen.Published()
	if err != nil {
		return err
	}
	if err := a.put(ctx, base+"/"+schemagen.FileName, published, "application/schema+json"); err != nil {
		return err
	}
	protos, err := fs.ReadDir(audit.Proto, fmt.Sprintf("proto/audit/v%d", major))
	if err != nil {
		return fmt.Errorf("writer: the proto of major %d is not embedded: %w", major, err)
	}
	for _, e := range protos {
		body, err := audit.Proto.ReadFile(path.Join(fmt.Sprintf("proto/audit/v%d", major), e.Name()))
		if err != nil {
			return err
		}
		if err := a.put(ctx, base+"/"+e.Name(), body, "text/plain"); err != nil {
			return err
		}
	}
	a.done.Store(mark, true)
	return nil
}

func (a *SchemaArchive) put(ctx context.Context, key string, body []byte, contentType string) error {
	now := time.Now().UTC()
	if a.Now != nil {
		now = a.Now().UTC()
	}
	retain := now.AddDate(10, 0, 0)
	if a.RetainUntil != nil {
		retain = a.RetainUntil(now)
	}
	err := a.Store.Put(ctx, store.Object{
		Key: key, Body: body, RetainUntil: retain, ContentType: contentType,
	})
	if errors.Is(err, store.ErrExists) {
		// A catalogue version is immutable by construction: a changed
		// catalogue is a new version. Finding the key taken means another
		// writer got there first, which is the outcome either way.
		return nil
	}
	return err
}

// schemaFileName turns a schema's identifier into a file name, keeping the last
// two path elements so that two schemas of one source do not collide.
func schemaFileName(id string) string {
	trimmed := id
	for _, cut := range []string{"https://", "http://"} {
		if len(trimmed) > len(cut) && trimmed[:len(cut)] == cut {
			trimmed = trimmed[len(cut):]
		}
	}
	name := path.Base(trimmed)
	if dir := path.Base(path.Dir(trimmed)); dir != "." && dir != "/" {
		name = dir + "-" + name
	}
	if path.Ext(name) == "" {
		name += ".json"
	}
	return name
}
