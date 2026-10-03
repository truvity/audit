package writer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sync"
	"time"

	"github.com/truvity/audit"
	"github.com/truvity/audit/internal/recobj"
	"github.com/truvity/audit/internal/schemagen"
	"github.com/truvity/audit/sdk/catalogue"
	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/store"
)

// ErrCatalogueConflict is returned when the archive holds a catalogue at a
// version whose bytes are not the ones the writer is about to run with. A
// catalogue version is immutable by construction, and a writer whose version
// means something else than what the archive holds would write records that
// name a description they do not follow: it refuses to run.
var ErrCatalogueConflict = errors.New("writer: the archive holds a different catalogue under this version")

// SchemaArchive copies into the archive whatever is needed to read it.
//
// A locked object outlives the deployment that wrote it, and very likely this
// repository. What sits beside it therefore has to be enough on its own: the
// catalogue that describes the actions, the extension schemas that describe
// their data, the record's own JSON Schema with the proto's comments carried
// as descriptions, and the proto itself for what a schema cannot say.
//
// The catalogue is the bucket contract's: catalogue/<app>/<version>, exactly as
// the application registered it, written once with a conditional put. The
// rest — the extension schemas and the record's schema and proto, under
// schema/ — is not in the contract, which says nothing against it: no part
// reads another's and a reader that wants only the contract ignores it.
//
// Copies are made on first use and never again. A catalogue version is
// immutable by construction — a changed catalogue is a new version — so a key
// already taken by the same bytes is the right answer and not a collision, and
// one taken by other bytes is a conflict.
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

// EnsureCatalogue writes the catalogue at catalogue/<app>/<version>, and its
// extension schemas beside the record's under schema/.
func (a *SchemaArchive) EnsureCatalogue(ctx context.Context, c *catalogue.Catalogue) error {
	mark := "catalogue/" + c.Source + "@" + c.Version
	if _, seen := a.done.Load(mark); seen {
		return nil
	}
	for what, part := range map[string]string{"source": c.Source, "version": c.Version} {
		if why := store.KeyComponent(part); why != "" {
			return fmt.Errorf("writer: the catalogue's %s %q %s, so it has no key", what, part, why)
		}
	}
	if err := a.putCatalogue(ctx, c); err != nil {
		return err
	}
	base := fmt.Sprintf("%s/%s/%s", SchemaPrefix, c.Source, c.Version)
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

// retainUntil is how long a description written now is kept.
func (a *SchemaArchive) retainUntil() time.Time {
	now := time.Now().UTC()
	if a.Now != nil {
		now = a.Now().UTC()
	}
	if a.RetainUntil != nil {
		return a.RetainUntil(now)
	}
	return now.AddDate(10, 0, 0)
}

// putCatalogue writes the catalogue document once. A key already present with
// the same bytes is success; with other bytes, a conflict.
func (a *SchemaArchive) putCatalogue(ctx context.Context, c *catalogue.Catalogue) error {
	key := store.CatalogueKey(c.Source, c.Version)
	body := c.Document()
	err := a.Store.Put(ctx, store.Object{
		Key: key, Body: body, RetainUntil: a.retainUntil(), ContentType: "application/yaml",
		Metadata: map[string]string{store.MetaSHA256: recobj.SHA256(body)},
	})
	if !errors.Is(err, store.ErrExists) {
		return err
	}
	held, err := a.Store.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("writer: reading %s: %w", key, err)
	}
	if !bytes.Equal(held, body) {
		return fmt.Errorf("%w: %s holds %d bytes (sha256 %s) and this writer has %d (sha256 %s)",
			ErrCatalogueConflict, key, len(held), recobj.SHA256(held), len(body), recobj.SHA256(body))
	}
	return nil
}

func (a *SchemaArchive) put(ctx context.Context, key string, body []byte, contentType string) error {
	err := a.Store.Put(ctx, store.Object{
		Key: key, Body: body, RetainUntil: a.retainUntil(), ContentType: contentType,
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
