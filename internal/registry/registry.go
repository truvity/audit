// Package registry holds the catalogues a deployment has registered.
//
// A catalogue is authored next to the code that emits against it and registered
// at deploy time, which is what keeps it true: a catalogue kept somewhere
// central and edited by hand drifts from the code within a release or two, and
// then the thing that describes the records is wrong about them.
//
// Registration is where the deployment gets its say. The document is validated
// against the same toolchain that validates it in the emitter's own tests, the
// source is checked against who is registering, and after each registration
// the categories the deployment's profiles require are checked against what
// every registered catalogue, and the component's own, carry together.
//
// Coverage is the deployment's property, not one application's: a profile
// needs authentication events from whoever signs people in and log access
// events from whoever reads the trail, and no single application does both. So
// a gap is reported (OnUncovered, and `audit validate --deployment` in the
// deployment's CI) rather than held against the application that happened to
// register while it was open.
package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/preset"
)

// Entry is a registered catalogue and what it was registered with.
type Entry struct {
	Source       string
	Version      string
	Document     []byte
	Schemas      map[string][]byte
	RegisteredAt time.Time
	RegisteredBy string
}

// Store keeps registered catalogues.
type Store interface {
	Put(ctx context.Context, e Entry) error
	Get(ctx context.Context, source, version string) (Entry, error)
	List(ctx context.Context) ([]Entry, error)
}

// ErrNotFound is returned for a catalogue that was never registered.
var ErrNotFound = errors.New("registry: no such catalogue")

// Registry validates and keeps catalogues.
type Registry struct {
	Store Store
	// Profiles are the deployment's, whose required categories the registered
	// catalogues together are checked against.
	Profiles map[string]*preset.Profile
	// Builtin are catalogues every deployment has without registering them:
	// the component's own. They count toward coverage.
	Builtin []*catalogue.Catalogue
	// OnUncovered is called after a registration for each profile whose
	// required categories nothing registered covers yet, so that the
	// deployment can say so where someone will read it.
	OnUncovered func(ctx context.Context, profile string, missing []string)
	// Identity is the verified source of whoever is registering. A transport
	// that cannot say returns "", and registration is then refused rather than
	// taking the document's word for whose it is.
	Identity func(ctx context.Context) string
	// OnRegistered is called for a catalogue that was accepted, so that the
	// deployment can record it.
	OnRegistered func(ctx context.Context, e Entry)
	Now          func() time.Time

	mu     sync.RWMutex
	cached map[string]*catalogue.Catalogue
}

// Register validates a catalogue and keeps it.
//
// It returns the problems rather than one error, because an application fixing
// its catalogue wants the whole list and not the first line of it.
func (r *Registry) Register(ctx context.Context, e Entry) ([]string, error) {
	if e.Source == "" || e.Version == "" {
		return []string{"a catalogue must name its source and version"}, nil
	}

	// Whose catalogue this is, is not the document's to claim. A workload that
	// could register under another source could describe another application's
	// records, and everything downstream reads the description.
	if r.Identity != nil {
		who := r.Identity(ctx)
		if who == "" {
			return []string{
				"the caller's source could not be verified, and a catalogue is not registered on its own say-so",
			}, nil
		}
		if who != e.Source {
			return []string{fmt.Sprintf(
				"%s may not register a catalogue for %s", who, e.Source)}, nil
		}
		e.RegisteredBy = who
	}

	loaded, problems := r.validate(e)
	if len(problems) > 0 {
		return problems, nil
	}

	// Registering twice is how a deployment rolls: every replica of an
	// application registers on start-up. The same document is accepted and
	// changes nothing; a different one under the same version is refused,
	// because a version that meant two things would make the archive's copy
	// and the emitter's copy disagree about records already written.
	if existing, err := r.Store.Get(ctx, e.Source, e.Version); err == nil {
		if !sameDocument(existing, e) {
			return []string{fmt.Sprintf(
				"%s version %s is already registered with a different document; "+
					"a version says what a record written under it means, so publish a new version instead",
				e.Source, e.Version)}, nil
		}
		return nil, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	e.RegisteredAt = r.now()
	if err := r.Store.Put(ctx, e); err != nil {
		return nil, err
	}
	r.cache(e.Source, e.Version, loaded)
	r.checkCoverage(ctx)
	if r.OnRegistered != nil {
		r.OnRegistered(ctx, e)
	}
	return nil, nil
}

// validate holds a catalogue to the toolchain and to the deployment.
func (r *Registry) validate(e Entry) (*catalogue.Catalogue, []string) {
	schemas := make([][]byte, 0, len(e.Schemas))
	for _, id := range sortedKeys(e.Schemas) {
		schemas = append(schemas, e.Schemas[id])
	}
	loaded, err := catalogue.Load(e.Document, schemas)
	if err != nil {
		return nil, []string{err.Error()}
	}
	var problems []string
	if loaded.Source != e.Source {
		problems = append(problems, fmt.Sprintf(
			"the document is for source %q and it was registered as %q", loaded.Source, e.Source))
	}
	if loaded.Version != e.Version {
		problems = append(problems, fmt.Sprintf(
			"the document is version %q and it was registered as %q", loaded.Version, e.Version))
	}

	return loaded, problems
}

// checkCoverage reports each profile's required categories that no catalogue
// of the deployment covers: the component's own, and the most recently
// registered version of every source. It reports; it does not refuse.
func (r *Registry) checkCoverage(ctx context.Context) {
	if r.OnUncovered == nil || len(r.Profiles) == 0 {
		return
	}
	entries, err := r.Store.List(ctx)
	if err != nil {
		return
	}
	latest := map[string]Entry{}
	for _, e := range entries {
		if held, ok := latest[e.Source]; !ok || e.RegisteredAt.After(held.RegisteredAt) {
			latest[e.Source] = e
		}
	}
	catalogues := append([]*catalogue.Catalogue(nil), r.Builtin...)
	for _, source := range sortedKeys(latest) {
		e := latest[source]
		if c, err := r.Get(ctx, e.Source, e.Version); err == nil {
			catalogues = append(catalogues, c)
		}
	}
	for _, name := range sortedProfiles(r.Profiles) {
		p := r.Profiles[name]
		if len(p.RequiredCategories) == 0 {
			continue
		}
		if missing := catalogue.MissingCategories(name, p.RequiredCategories, catalogues); len(missing) > 0 {
			r.OnUncovered(ctx, name, missing)
		}
	}
}

// Get implements the writer's Catalogues: it resolves the catalogue a record
// names, from the registry, with a cache because the writer asks per record.
func (r *Registry) Get(ctx context.Context, source, version string) (*catalogue.Catalogue, error) {
	r.mu.RLock()
	found, ok := r.cached[source+"@"+version]
	r.mu.RUnlock()
	if ok {
		return found, nil
	}

	e, err := r.Store.Get(ctx, source, version)
	if err != nil {
		return nil, err
	}
	schemas := make([][]byte, 0, len(e.Schemas))
	for _, id := range sortedKeys(e.Schemas) {
		schemas = append(schemas, e.Schemas[id])
	}
	loaded, err := catalogue.Load(e.Document, schemas)
	if err != nil {
		return nil, fmt.Errorf("registry: %s %s: %w", source, version, err)
	}
	r.cache(source, version, loaded)
	return loaded, nil
}

// Entry returns a registered catalogue as it was registered.
func (r *Registry) Entry(ctx context.Context, source, version string) (Entry, error) {
	return r.Store.Get(ctx, source, version)
}

// List returns every registered catalogue.
func (r *Registry) List(ctx context.Context) ([]Entry, error) { return r.Store.List(ctx) }

func (r *Registry) cache(source, version string, c *catalogue.Catalogue) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cached == nil {
		r.cached = map[string]*catalogue.Catalogue{}
	}
	r.cached[source+"@"+version] = c
}

func (r *Registry) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func sameDocument(a, b Entry) bool {
	if string(a.Document) != string(b.Document) || len(a.Schemas) != len(b.Schemas) {
		return false
	}
	for id, body := range b.Schemas {
		if string(a.Schemas[id]) != string(body) {
			return false
		}
	}
	return true
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedProfiles(m map[string]*preset.Profile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
