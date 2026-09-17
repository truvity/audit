// Package preset reads the framework presets and composes them into profiles.
//
// A preset says what one framework requires of a copy: which fields it carries,
// how identities are treated, how long it is kept, what integrity controls
// apply, and how often it is reviewed. A profile is a deployment's composition
// of presets and is what the split writer produces one copy for.
//
// Nothing here is legal advice. A preset is an engineering reading of the
// clauses it cites, and a deployer confirms applicability with its own
// assessor.
package preset

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/truvity/audit"
	"github.com/truvity/audit/internal/metaschema"
)

// Treatment is what happens to an identifier of a given actor category in a
// copy under this preset.
type Treatment string

const (
	// Clear keeps the identifier as it is. Accountability for staff and
	// machines is a legal obligation, so their identifiers are readable.
	Clear Treatment = "clear"
	// Pseudonym replaces the identifier with a keyed pseudonym, per tenant and
	// per purpose, so copies cannot be joined on a person.
	Pseudonym Treatment = "pseudonym"
	// Scoped keeps the source's own identifier, which is meaningful only inside
	// the tenant that already knows its people.
	Scoped Treatment = "scoped"
	// Omit carries no identifier at all.
	Omit Treatment = "omit"
)

// strictness orders treatments so that composing presets can take the strictest.
var strictness = map[Treatment]int{Clear: 0, Scoped: 1, Pseudonym: 2, Omit: 3}

// Category is the kind of actor a treatment applies to. An actor kind declares
// its category in the catalogue; the treatment follows the category, never a
// field name.
type Category string

// Internal, External and Machine are the actor categories a preset sets a
// treatment for.
const (
	Internal Category = "internal" // staff, operators
	External Category = "external" // end users, a customer's people
	Machine  Category = "machine"  // services, API keys, the system
)

// Class is the field class an extension property declares with x-audit-class.
type Class string

// Shared, Audit, Metering, History and Evidence are the field classes an
// extension property declares and a preset keeps.
const (
	Shared   Class = "shared"
	Audit    Class = "audit"
	Metering Class = "metering"
	History  Class = "history"
	Evidence Class = "evidence"
)

// Preset is one framework's requirements.
type Preset struct {
	Name       string     `json:"name"`
	Framework  string     `json:"framework"`
	Version    string     `json:"version"`
	Summary    string     `json:"summary,omitempty"`
	Disclaimer string     `json:"disclaimer"`
	Citations  []Citation `json:"citations"`

	FieldClasses       []Class  `json:"field_classes"`
	RequiredFields     []string `json:"required_fields"`
	OptionalFields     []string `json:"optional_fields,omitempty"`
	ForbiddenFields    []string `json:"forbidden_fields,omitempty"`
	ForbiddenPII       []string `json:"forbidden_pii,omitempty"`
	RequiredCategories []string `json:"required_categories,omitempty"`

	Identity  map[Category]Treatment `json:"identity"`
	Retention Retention              `json:"retention"`
	Integrity Integrity              `json:"integrity"`
	Review    Review                 `json:"review,omitempty"`
	Pipeline  Pipeline               `json:"pipeline,omitempty"`
}

// Citation is the clause a preset reads and where to read it.
type Citation struct {
	Clause string `json:"clause"`
	URL    string `json:"url"`
	Note   string `json:"note,omitempty"`
}

// Retention is how long a copy is kept and how much of that must be quick to
// search.
type Retention struct {
	Policy           string `json:"policy"` // fixed | after_expiry
	Days             int    `json:"days,omitempty"`
	YearsAfterExpiry int    `json:"years_after_expiry,omitempty"`
	FallbackDays     int    `json:"fallback_days,omitempty"`
	HotDays          int    `json:"hot_days,omitempty"`
	Configurable     bool   `json:"configurable,omitempty"`
	MinimumDays      int    `json:"minimum_days,omitempty"`
	PublishedInTerms bool   `json:"published_in_terms,omitempty"`
	DeleteAtEnd      bool   `json:"delete_at_end,omitempty"`
	Note             string `json:"note,omitempty"`
}

// Integrity is what must be true of the store a copy lands in.
type Integrity struct {
	Digest          string `json:"digest,omitempty"`           // required | recommended
	ObjectLockMode  string `json:"object_lock_mode,omitempty"` // compliance | governance
	TimestampAnchor string `json:"timestamp_anchor,omitempty"` // none | recommended | required
	LegalHold       string `json:"legal_hold,omitempty"`       // available | recommended
	ClockSyncEvent  string `json:"clock_sync_event,omitempty"` // none | daily
	LogAccessLogged bool   `json:"log_access_logged,omitempty"`
}

// Review is how often the trail is read by a person and what is kept to show
// that it was.
type Review struct {
	AutomatedAlerting bool   `json:"automated_alerting,omitempty"`
	Cadence           string `json:"cadence,omitempty"`
	Evidence          string `json:"evidence,omitempty"`
}

// Pipeline is what the write path must guarantee for this preset.
type Pipeline struct {
	DedupeWindowDays  int `json:"dedupe_window_days,omitempty"`
	StreamHorizonDays int `json:"stream_horizon_days,omitempty"`
	CloseAfterHours   int `json:"close_after_hours,omitempty"`
}

// Load reads and validates one preset document.
func Load(data []byte) (*Preset, error) {
	if err := metaschema.Validate("preset.schema.json", data); err != nil {
		return nil, err
	}
	var p Preset
	if err := yaml.UnmarshalStrict(data, &p); err != nil {
		return nil, fmt.Errorf("preset: %w", err)
	}
	if err := p.check(); err != nil {
		return nil, fmt.Errorf("preset %s: %w", p.Name, err)
	}
	return &p, nil
}

// LoadDir reads every .yaml preset in a directory.
func LoadDir(fsys fs.FS, dir string) (map[string]*Preset, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*Preset, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		p, err := Load(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if _, seen := out[p.Name]; seen {
			return nil, fmt.Errorf("preset %s is declared twice", p.Name)
		}
		out[p.Name] = p
	}
	return out, nil
}

// Builtin returns the presets this repository ships.
func Builtin() (map[string]*Preset, error) { return LoadDir(audit.Presets, "presets") }

// check is what the meta-schema cannot express.
func (p *Preset) check() error {
	var problems []error
	req := index(p.RequiredFields)
	for _, f := range p.ForbiddenFields {
		if req[f] {
			problems = append(problems, fmt.Errorf("field %s is both required and forbidden", f))
		}
	}
	for _, c := range []Category{Internal, External, Machine} {
		t, ok := p.Identity[c]
		if !ok {
			problems = append(problems, fmt.Errorf("identity.%s is not set", c))
			continue
		}
		if _, known := strictness[t]; !known {
			problems = append(problems, fmt.Errorf("identity.%s is %q, which is not a treatment", c, t))
		}
	}
	switch p.Retention.Policy {
	case "fixed":
		if p.Retention.Days <= 0 {
			problems = append(problems, errors.New("retention.days is required under a fixed policy"))
		}
	case "after_expiry":
		if p.Retention.YearsAfterExpiry <= 0 {
			problems = append(problems, errors.New("retention.years_after_expiry is required under an after_expiry policy"))
		}
		if p.Retention.FallbackDays <= 0 {
			problems = append(problems, errors.New("retention.fallback_days is required: an expiry is not always known when the record is written"))
		}
	}
	if len(p.Citations) == 0 {
		problems = append(problems, errors.New("a preset states what a framework requires and must cite it"))
	}
	if strings.TrimSpace(p.Disclaimer) == "" {
		problems = append(problems, errors.New("a preset is an engineering reading and must say so"))
	}
	return errors.Join(problems...)
}

func index(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
