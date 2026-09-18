package writer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/emit"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// Catalogues resolves the catalogue a record names.
//
// A record carries the source and version of what describes it, and the writer
// takes no emitter's word for what a record contains: it validates against the
// same catalogue the emitter did, resolved here rather than trusted from there.
type Catalogues interface {
	Get(ctx context.Context, source, version string) (*catalogue.Catalogue, error)
}

// Registry is a Catalogues backed by whatever has been registered.
type Registry struct {
	mu         sync.RWMutex
	catalogues map[string]*catalogue.Catalogue
}

// Register adds a catalogue version.
func (r *Registry) Register(c *catalogue.Catalogue) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.catalogues == nil {
		r.catalogues = map[string]*catalogue.Catalogue{}
	}
	r.catalogues[c.Source+"@"+c.Version] = c
}

// Get implements Catalogues.
func (r *Registry) Get(_ context.Context, source, version string) (*catalogue.Catalogue, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.catalogues[source+"@"+version]
	if !ok {
		return nil, fmt.Errorf("no catalogue %s version %s is registered", source, version)
	}
	return c, nil
}

// Hooks are where a deployment attaches its metrics and alerts.
type Hooks struct {
	// OnWritten is called for each object put.
	OnWritten func(key string, records int)
	// OnDeadLettered is called for each record the writer could not process.
	// A deployment that does not alert on this is one where a fault upstream
	// is silent until somebody goes looking.
	OnDeadLettered func(r *record.Record, reason string)
	// OnDuplicate is called for each record seen before.
	OnDuplicate func(r *record.Record)
	// OnDuplicatesLikely is called when records were written but could not be
	// marked as written. Nothing is lost; a redelivery of those records will
	// be taken again and the archive will hold a second copy of each, which
	// the index absorbs by identifier. A deployment watches this because a
	// store that has stopped accepting marks stops deduplicating entirely.
	OnDuplicatesLikely func(ids []string, err error)
	// OnUnhandled is called with the profiles an action names that this
	// deployment does not have.
	OnUnhandled func(action string, profiles []string)
	// OnMetaDropped is called when the writer's account of itself could not be
	// recorded. It is best-effort by construction, so this is the only place a
	// deployment learns that the writer's own trail has a hole in it.
	OnMetaDropped func(action, reason string)
}

// Writer takes records and puts the copies their profiles keep.
//
// It is a sink like every other hop, which is what lets it sit behind a queue,
// behind a Connect handler, or inside the application itself without anything
// else changing.
type Writer struct {
	Catalogues Catalogues
	Splitter   *Splitter
	Roller     *Roller
	Dedupe     Dedupe
	DeadLetter DeadLetter
	// Archive copies what a reader needs to make sense of the records: the
	// catalogue, its extension schemas, and the record's own schema and proto.
	// Without it the archive is a heap of JSON whose meaning lives somewhere
	// else.
	Archive *SchemaArchive
	// Meta is the catalogue of the writer's own actions, normally the common
	// one. Given it, the writer keeps an account of itself in the archive it
	// writes: see meta.go for why that loop is the right one. Without it the
	// writer is silent about itself, and a reader cannot tell a quiet hour from
	// a stopped writer.
	Meta  *catalogue.Catalogue
	Hooks Hooks

	// Identity returns the verified identity of whoever published, which the
	// writer stamps on the record. A transport that cannot say returns "", and
	// the record then carries no observer identity rather than a claimed one.
	Identity func(ctx context.Context) string

	// Version names this writer in the records it stamps.
	Version string
	// Now is the clock, for tests.
	Now func() time.Time

	self      *emit.Emitter
	unhandled sync.Map
}

// New checks a writer's parts before it takes anything.
func New(w *Writer) (*Writer, error) {
	switch {
	case w.Catalogues == nil:
		return nil, errors.New("writer: a catalogue source is required")
	case w.Splitter == nil:
		return nil, errors.New("writer: a splitter is required")
	case w.Roller == nil:
		return nil, errors.New("writer: a roller is required")
	case w.DeadLetter == nil:
		return nil, errors.New("writer: a dead letter store is required: nothing may be dropped")
	}
	if w.Dedupe == nil {
		w.Dedupe = &MemoryDedupe{}
	}
	if w.Meta != nil {
		// The writer resolves every record's catalogue through Catalogues,
		// including its own. A meta catalogue that is not also registered there
		// would make every one of the writer's own records dead-letter, and
		// because a meta dead letter is not emitted about, it would do so in
		// silence. Refusing to start is the only honest answer.
		if _, err := w.Catalogues.Get(context.Background(), w.Meta.Source, w.Meta.Version); err != nil {
			return nil, fmt.Errorf(
				"writer: the writer's own catalogue %s %s must be registered like any other: %w",
				w.Meta.Source, w.Meta.Version, err)
		}
		if err := w.startMeta(w.Meta); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// Write implements sink.Sink.
//
// A batch is taken as a whole: every record in it is either written, seen
// before, or dead-lettered, and only then does the call return. A caller that
// gets no error may forget the batch, which is what the acknowledgement above
// this depends on.
func (w *Writer) Write(ctx context.Context, req *sink.Request) (*sink.Result, error) {
	if len(req.Records) == 0 {
		return &sink.Result{}, nil
	}
	ids := make([]string, 0, len(req.Records))
	for _, r := range req.Records {
		ids = append(ids, r.GetId())
	}
	seen, err := w.Dedupe.Seen(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("writer: %w", err)
	}

	result := &sink.Result{}
	written := make([]string, 0, len(req.Records))
	inBatch := make(map[string]bool, len(req.Records))
	for _, r := range req.Records {
		// A batch may carry the same record twice — a redelivery bundled with
		// the original is an ordinary shape. Asking the store does not settle
		// that, because the store is not told anything until the batch is
		// durable, so the batch keeps its own account of what it has taken.
		if seen[r.GetId()] || inBatch[r.GetId()] {
			// Every hop below is at-least-once on purpose. This is where the
			// repeats stop.
			result.Accepted++
			if w.Hooks.OnDuplicate != nil {
				w.Hooks.OnDuplicate(r)
			}
			continue
		}
		if err := w.one(ctx, r); err != nil {
			return nil, err
		}
		if id := r.GetId(); id != "" {
			inBatch[id] = true
			written = append(written, id)
		}
		result.Accepted++
	}

	// Nothing is acknowledged before it is in the archive. Holding a batch open
	// in memory and telling the caller it is safe is the one way this component
	// could lose a record while reporting success.
	if err := w.Roller.Flush(ctx); err != nil {
		return nil, err
	}

	// Only now, with the copies durable, are the identifiers marked. See the
	// Dedupe interface for why this is not done before the write.
	if err := w.Dedupe.Mark(ctx, written); err != nil {
		// The records are in the archive; what failed is the note that says so.
		// Failing the batch here would ask the caller to redeliver records that
		// are already written, which is the one thing marking afterwards is
		// meant to keep rare. A deployment watches this instead.
		if w.Hooks.OnDuplicatesLikely != nil {
			w.Hooks.OnDuplicatesLikely(written, err)
		}
	}
	return result, nil
}

// one processes a single record: resolve, validate, stamp, split, gather.
func (w *Writer) one(ctx context.Context, r *record.Record) error {
	c, err := w.Catalogues.Get(ctx, r.GetSource(), r.GetCatalogueVersion())
	if err != nil {
		return w.deadLetter(ctx, r, err.Error())
	}
	if w.Archive != nil {
		// Before the first record of a catalogue version lands, what describes
		// it is beside it. Doing this after would leave a window in which the
		// archive holds records nothing explains.
		if err := w.Archive.EnsureCatalogue(ctx, c); err != nil {
			return fmt.Errorf("writer: archive catalogue %s %s: %w", c.Source, c.Version, err)
		}
		if err := w.Archive.EnsureRecord(ctx, r.GetSchemaVersion()); err != nil {
			return fmt.Errorf("writer: archive record schema: %w", err)
		}
	}
	x, err := c.Compose(r.GetAction())
	if err != nil {
		return w.deadLetter(ctx, r, err.Error())
	}

	// The writer takes no emitter's word for what a record contains.
	if err := x.Validate(r); err != nil {
		return w.deadLetter(ctx, r, err.Error())
	}

	observer := &record.Observer{
		Id:       w.identity(ctx),
		Version:  r.GetObserver().GetVersion(),
		Instance: r.GetObserver().GetInstance(),
	}
	if err := record.Stamp(r, observer, w.now()); err != nil {
		return w.deadLetter(ctx, r, err.Error())
	}

	// When the credential the record is about expires, if its catalogue says
	// where to read it. Read from the record as written, before a profile's
	// copy drops the data slot, because it decides how long every copy is kept.
	// A value marked as the expiry that is not a time is a fault in the record,
	// not in the writer, and is dead-lettered like any other.
	expiry, err := x.Expiry(r)
	if err != nil {
		return w.deadLetter(ctx, r, err.Error())
	}

	if unhandled := w.Splitter.Unhandled(x); len(unhandled) > 0 {
		w.reportUnhandled(r.GetAction(), unhandled)
	}
	copies, err := w.Splitter.Split(ctx, r, x)
	if err != nil {
		// A failure to split is a failure of configuration, not of the record.
		// Dead-lettering would blame the wrong thing and lose the record's
		// place in the stream, so the batch fails and the caller retries.
		return fmt.Errorf("writer: split %s: %w", r.GetAction(), err)
	}
	if len(copies) == 0 {
		// An action whose profiles this deployment has none of is not an error,
		// but the record has nowhere to go and must not disappear.
		return w.deadLetter(ctx, r, "no configured profile keeps this action")
	}
	fields := indexFields(x)
	for _, copied := range copies {
		profile := w.Splitter.Profiles[copied.GetProfile()]
		if err := w.Roller.AddExpiring(ctx, profile, copied, fields, expiry); err != nil {
			return err
		}
	}
	return nil
}

// indexFields is what the action's catalogue says about its data slot: which
// properties a query may name, and which a searcher may count. The index takes
// the answer rather than the catalogue, so that an implementation of the
// Indexer interface needs neither.
func indexFields(x *catalogue.Composed) index.Fields {
	if x.Data == nil {
		return index.Fields{}
	}
	return index.Fields{Filter: x.Data.Filterable(), Facet: x.Data.Facets()}
}

func (w *Writer) deadLetter(ctx context.Context, r *record.Record, reason string) error {
	if err := w.DeadLetter.Put(ctx, r, reason); err != nil {
		return fmt.Errorf("writer: dead letter: %w", err)
	}
	if w.Hooks.OnDeadLettered != nil {
		w.Hooks.OnDeadLettered(r, reason)
	}
	if _, self := metaInstance(ctx); !self {
		// A meta-record that cannot be written is kept and reported, never
		// emitted about: one bad meta-record would otherwise beget another for
		// as long as the process ran.
		w.metaDeadLettered(ctx, r, reason)
	}
	return nil
}

// reportUnhandled tells an operator once per action, not once per record.
func (w *Writer) reportUnhandled(action string, profiles []string) {
	if _, told := w.unhandled.LoadOrStore(action, true); told {
		return
	}
	if w.Hooks.OnUnhandled != nil {
		w.Hooks.OnUnhandled(action, profiles)
	}
}

func (w *Writer) identity(ctx context.Context) string {
	if instance, self := metaInstance(ctx); self {
		// The writer's own identity is the one it need take nobody's word for.
		return instance
	}
	if w.Identity == nil {
		return ""
	}
	return w.Identity(ctx)
}

func (w *Writer) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

// Close flushes whatever is still gathered.
//
// The writer's last act is to say that it stopped. That record is queued, then
// the emitter is closed, which drains it back into this writer, and only then
// is the archive flushed: a stopped event written after the flush would be a
// stopped event nobody could read.
func (w *Writer) Close(ctx context.Context) error {
	if w.self != nil {
		w.stopped(ctx)
		if err := w.self.Close(); err != nil {
			return fmt.Errorf("writer: closing its own emitter: %w", err)
		}
	}
	if err := w.Roller.Flush(ctx); err != nil {
		return err
	}
	return w.Roller.Close()
}
