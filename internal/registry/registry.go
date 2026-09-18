// Package registry holds the catalogues a deployment has registered.
//
// A catalogue is authored next to the code that emits against it and registered
// at deploy time, which is what keeps it true: a catalogue kept somewhere
// central and edited by hand drifts from the code within a release or two, and
// then the thing that describes the records is wrong about them.
//
// Registration is where the deployment gets its say. The document is validated
// against the same toolchain that validates it in the emitter's own tests, the
// source is checked against who is registering, and the categories the
// deployment's profiles require are checked against what the catalogue's
// actions carry. An application cannot register a catalogue that would leave a
// profile unable to meet its framework.
package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
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
	// Profiles are the deployment's, whose required categories every registered
	// catalogue is checked against.
	Profiles map[string]*preset.Profile
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

	// The deployment's own requirement: a profile exists to satisfy a
	// framework, and a framework requires certain categories of event. A
	// catalogue whose actions leave one of those categories uncovered would let
	// the profile look complete while missing what it is for.
	for _, name := range sortedProfiles(r.Profiles) {
		p := r.Profiles[name]
		if len(p.RequiredCategories) == 0 {
			continue
		}
		missing := catalogue.MissingCategories(name, p.RequiredCategories, []*catalogue.Catalogue{loaded})
		if len(missing) > 0 && emitsInto(loaded, name) {
			problems = append(problems, fmt.Sprintf(
				"profile %s requires %s and no action of this catalogue that lands in it carries them",
				name, strings.Join(missing, ", ")))
		}
	}
	return loaded, problems
}

// emitsInto reports whether any action of a catalogue lands in a profile. A
// catalogue that never writes into a profile is not the one that has to satisfy
// it.
func emitsInto(c *catalogue.Catalogue, profile string) bool {
	for _, name := range c.ActionNames() {
		a, _ := c.Action(name)
		for _, p := range a.Profiles {
			if p == profile {
				return true
			}
		}
	}
	return false
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

func sortedKeys(m map[string][]byte) []string {
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
