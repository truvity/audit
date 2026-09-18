// Package catalogue reads and validates the action catalogues that describe
// what a source emits.
//
// A catalogue is authored next to the code that emits, validated in that code's
// own tests, registered at deploy, and copied into the archive on first use. It
// is what makes the rest of the system generic: the split writer, the indexer,
// the viewer and the exporters read a catalogue rather than an application's
// code.
package catalogue

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/truvity/audit"
	"github.com/truvity/audit/preset"
)

// Catalogue is what one source emits.
type Catalogue struct {
	Source       string                `json:"source"`
	Version      string                `json:"version"`
	Locales      []string              `json:"locales,omitempty"`
	ActorKinds   map[string]ActorKind  `json:"actor_kinds,omitempty"`
	TargetTypes  map[string]TargetType `json:"target_types,omitempty"`
	ContextAreas map[string]string     `json:"context_areas,omitempty"`
	Meters       map[string]MeterDef   `json:"meters,omitempty"`
	Actions      map[string]Action     `json:"actions"`

	// schemas are the extension schemas this catalogue references, by $id.
	schemas map[string]*Schema
	// document is the catalogue as it was registered. It is kept because the
	// archive keeps a copy of it beside the records it describes, and a
	// re-serialised catalogue would not be the one that was registered.
	document []byte
}

// Document returns the catalogue as it was registered.
func (c *Catalogue) Document() []byte { return c.document }

// Schemas returns every extension schema, by $id, as registered.
func (c *Catalogue) Schemas() map[string][]byte {
	out := make(map[string][]byte, len(c.schemas))
	for id, s := range c.schemas {
		out[id] = s.Raw
	}
	return out
}

// ActorKind declares a kind of actor and, through its category, how its
// identifier is treated in each profile. The treatment follows the category,
// never a field name, so adding a kind cannot quietly widen what is kept.
type ActorKind struct {
	Category         preset.Category `json:"category"`
	Description      string          `json:"description,omitempty"`
	AttributesSchema string          `json:"attributes_schema,omitempty"`
}

// TargetType declares a kind of thing an action is done to.
type TargetType struct {
	Description      string `json:"description,omitempty"`
	IsPerson         bool   `json:"is_person,omitempty"`
	AttributesSchema string `json:"attributes_schema,omitempty"`
}

// MeterDef declares a unit of usage. A count is measured by events; a gauge is
// measured by absolute samples on a schedule, never by deltas, so a lost sample
// costs accuracy rather than correctness.
type MeterDef struct {
	Kind             string `json:"kind"` // count | gauge
	Unit             string `json:"unit"`
	Description      string `json:"description,omitempty"`
	DimensionsSchema string `json:"dimensions_schema,omitempty"`
}

// Action is one thing that can happen, and everything the system needs to know
// about it without reading the code that emits it.
type Action struct {
	Summary      string            `json:"summary"`
	Operation    string            `json:"operation"`
	Categories   []string          `json:"categories,omitempty"`
	Profiles     []string          `json:"profiles"`
	CaptureLevel string            `json:"capture_level,omitempty"`
	Delivery     string            `json:"delivery,omitempty"`
	TargetTypes  []string          `json:"target_types,omitempty"`
	DataSchema   string            `json:"data_schema,omitempty"`
	DataVersion  string            `json:"data_version,omitempty"`
	Message      map[string]string `json:"message,omitempty"`
	Meter        *ActionMeter      `json:"meter,omitempty"`
	// Extends names the data property that holds the identifiers of earlier
	// records this one is an addendum to: a renewal naming the issuance, a
	// credential naming the identity proofing it relied on. Those records are
	// evidence for as long as what relies on them lives, so the writer
	// lengthens the lock on the objects holding them to this record's expiry
	// plus the profile's years. The property is a string or an array of them,
	// and the schema must mark an expiry, or there is nothing to extend to.
	Extends string `json:"extends,omitempty"`
}

// ActionMeter binds an action to a meter and says where its quantity comes
// from. A meter with no quantity path counts one per event.
type ActionMeter struct {
	Name         string `json:"name"`
	QuantityPath string `json:"quantity_path,omitempty"`
	// Outcomes are the outcomes that count. A refused call is not a billable
	// one, so an absent list means success only; an action that bills
	// attempts says so.
	Outcomes []string `json:"outcomes,omitempty"`
}

// Counts reports whether a record with this outcome contributes to the meter.
func (m *ActionMeter) Counts(outcome string) bool {
	if m == nil {
		return false
	}
	if len(m.Outcomes) == 0 {
		return outcome == "success"
	}
	for _, o := range m.Outcomes {
		if o == outcome {
			return true
		}
	}
	return false
}

// Load reads a catalogue document together with the extension schemas it
// references, keyed by their $id.
func Load(doc []byte, schemas [][]byte) (*Catalogue, error) {
	if err := validateAgainst("catalogue.schema.json", doc); err != nil {
		return nil, err
	}
	var c Catalogue
	if err := yaml.UnmarshalStrict(doc, &c); err != nil {
		return nil, fmt.Errorf("catalogue: %w", err)
	}
	c.schemas = map[string]*Schema{}
	for _, raw := range schemas {
		s, err := LoadSchema(raw)
		if err != nil {
			return nil, fmt.Errorf("catalogue %s: %w", c.Source, err)
		}
		if _, seen := c.schemas[s.ID]; seen {
			return nil, fmt.Errorf("catalogue %s: two schemas claim %s", c.Source, s.ID)
		}
		c.schemas[s.ID] = s
	}
	c.document = append([]byte(nil), doc...)
	if err := c.check(); err != nil {
		return nil, fmt.Errorf("catalogue %s %s: %w", c.Source, c.Version, err)
	}
	return &c, nil
}

// LoadFS reads a catalogue document and every .json schema beside it in the
// same directory.
func LoadFS(fsys fs.FS, doc string) (*Catalogue, error) {
	raw, err := fs.ReadFile(fsys, doc)
	if err != nil {
		return nil, err
	}
	dir := path.Dir(doc)
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var schemas [][]byte
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		schemas = append(schemas, b)
	}
	return Load(raw, schemas)
}

// Common is the catalogue of the component's own events: reads and exports of
// the trail, registrations, profile and preset changes, key destruction, legal
// holds, digests, writer lifecycle and the daily clock check. Every deployment
// carries it.
func Common() (*Catalogue, error) { return LoadFS(audit.Catalogue, "catalogue/common.yaml") }

// Schema returns a referenced extension schema.
func (c *Catalogue) Schema(id string) (*Schema, bool) {
	s, ok := c.schemas[id]
	return s, ok
}

// Action returns an action by its full name.
func (c *Catalogue) Action(name string) (Action, bool) {
	a, ok := c.Actions[name]
	return a, ok
}

// ActionNames returns every action this catalogue declares, in order.
func (c *Catalogue) ActionNames() []string {
	names := make([]string, 0, len(c.Actions))
	for n := range c.Actions {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Categories returns every framework category this catalogue covers, which is
// what a preset's required categories are checked against.
func (c *Catalogue) Categories() map[string]bool {
	out := map[string]bool{}
	for _, a := range c.Actions {
		for _, cat := range a.Categories {
			out[cat] = true
		}
	}
	return out
}

// check is everything the meta-schema cannot express.
func (c *Catalogue) check() error {
	var problems []error
	fail := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }

	referenced := map[string]bool{}
	useSchema := func(where, id string) {
		if id == "" {
			return
		}
		referenced[id] = true
		if _, ok := c.schemas[id]; !ok {
			fail("%s references schema %s, which was not supplied", where, id)
		}
	}

	for name, k := range c.ActorKinds {
		switch k.Category {
		case preset.Internal, preset.External, preset.Machine:
		default:
			fail("actor kind %s has category %q, which is not internal, external or machine", name, k.Category)
		}
		useSchema("actor kind "+name, k.AttributesSchema)
	}
	for name, t := range c.TargetTypes {
		useSchema("target type "+name, t.AttributesSchema)
	}
	for area, id := range c.ContextAreas {
		useSchema("context area "+area, id)
	}
	for name, m := range c.Meters {
		if m.Kind != "count" && m.Kind != "gauge" {
			fail("meter %s has kind %q, which is not count or gauge", name, m.Kind)
		}
		useSchema("meter "+name, m.DimensionsSchema)
	}

	for name, a := range c.Actions {
		if !strings.HasPrefix(name, c.Source+".") {
			fail("action %s is not under the namespace of source %s", name, c.Source)
		}
		useSchema("action "+name, a.DataSchema)
		for _, tt := range a.TargetTypes {
			if _, ok := c.TargetTypes[tt]; !ok {
				fail("action %s names target type %q, which this catalogue does not declare", name, tt)
			}
		}
		if a.Extends != "" {
			problems = append(problems, c.checkExtends(name, a)...)
		}
		if a.Meter != nil {
			m, ok := c.Meters[a.Meter.Name]
			if !ok {
				fail("action %s meters %q, which this catalogue does not declare", name, a.Meter.Name)
			} else if m.Kind == "gauge" && a.Meter.QuantityPath == "" {
				fail("action %s samples gauge %q but names no quantity path", name, a.Meter.Name)
			}
		}
		problems = append(problems, c.checkMessages(name, a)...)
	}

	for id := range c.schemas {
		if !referenced[id] {
			fail("schema %s is supplied but nothing references it", id)
		}
	}
	return errors.Join(problems...)
}

// checkExtends holds an addendum to what the writer needs to act on it: a
// property it can read identifiers from, and an expiry to extend to. Either
// missing would make the declaration a promise the writer quietly breaks.
func (c *Catalogue) checkExtends(name string, a Action) []error {
	s, ok := c.schemas[a.DataSchema]
	if !ok {
		return []error{fmt.Errorf("action %s extends earlier records but has no data schema to name them in", name)}
	}
	var problems []error
	p, ok := s.Properties[a.Extends]
	switch {
	case !ok:
		problems = append(problems, fmt.Errorf(
			"action %s extends the records named by %s, which its data schema does not declare", name, a.Extends))
	case p.Type != "string" && p.Type != "array":
		problems = append(problems, fmt.Errorf(
			"action %s extends the records named by %s, which is a %s rather than an identifier or a list of them",
			name, a.Extends, p.Type))
	}
	expiry := false
	for _, p := range s.Properties {
		expiry = expiry || p.Expiry
	}
	if !expiry {
		problems = append(problems, fmt.Errorf(
			"action %s extends earlier records but its data schema marks no x-audit-expiry to extend them to", name))
	}
	return problems
}

// checkMessages holds templates to what they may say. A template that names an
// argument no record carries renders as a gap in the viewer, which is the one
// place a reader is entitled to a straight sentence.
func (c *Catalogue) checkMessages(name string, a Action) []error {
	var problems []error
	locales := c.Locales
	if len(locales) == 0 {
		locales = []string{"en"}
	}
	for _, locale := range locales {
		template, ok := a.Message[locale]
		if !ok || strings.TrimSpace(template) == "" {
			problems = append(problems, fmt.Errorf("action %s has no message for locale %q", name, locale))
			continue
		}
		allowed := c.messageArguments(a)
		for _, arg := range MessageArguments(template) {
			switch {
			case allowed[arg]:
			case strings.Contains(arg, "."):
				// ICU forbids a dot in an argument name, so a template with one
				// is valid to nobody's renderer.
				problems = append(problems, fmt.Errorf(
					"action %s, locale %s: the template names %q; argument names use underscores, not dots: %q",
					name, locale, arg, strings.ReplaceAll(arg, ".", "_")))
			default:
				problems = append(problems, fmt.Errorf(
					"action %s, locale %s: the template names %q, which a record of this action does not carry", name, locale, arg))
			}
		}
	}
	return append(problems, c.argumentCollisions(name, a)...)
}

// argumentCollisions refuses a data schema two of whose properties would
// answer to the same template argument: /a_b and /a/b are both data_a_b.
func (c *Catalogue) argumentCollisions(name string, a Action) []error {
	s, ok := c.schemas[a.DataSchema]
	if !ok {
		return nil
	}
	seen := map[string]string{}
	var problems []error
	pointers := make([]string, 0, len(s.Properties))
	for pointer := range s.Properties {
		pointers = append(pointers, pointer)
	}
	sort.Strings(pointers)
	for _, pointer := range pointers {
		arg := DataArgument(pointer)
		if other, taken := seen[arg]; taken {
			problems = append(problems, fmt.Errorf(
				"action %s: data properties %s and %s would both be the template argument %s; rename one",
				name, other, pointer, arg))
			continue
		}
		seen[arg] = pointer
	}
	return problems
}

// DataArgument is the template argument that names a data property: its JSON
// pointer under data, with each step joined by an underscore, so that the
// name is one ICU accepts. /items is data_items; /address/city is
// data_address_city.
func DataArgument(pointer string) string {
	return "data" + strings.ReplaceAll(pointer, "/", "_")
}

// messageArguments is what a template of this action may name: the core fields
// a reader can count on, and the properties the action's own schema declares.
func (c *Catalogue) messageArguments(a Action) map[string]bool {
	allowed := map[string]bool{}
	for _, core := range []string{
		"id", "source", "action", "operation", "tenant", "profile",
		"actor", "actor_id", "actor_kind", "subject", "subject_id", "subject_kind",
		"outcome", "outcome_result", "outcome_reason", "outcome_code",
		"observer_id", "observer_instance", "occurred_at", "recorded_at",
	} {
		allowed[core] = true
	}
	// A template may name the first few targets positionally; beyond that a
	// sentence stops being a sentence.
	for i := 0; i < 4; i++ {
		for _, part := range []string{"id", "name", "type"} {
			allowed[fmt.Sprintf("targets_%d_%s", i, part)] = true
		}
	}
	if s, ok := c.schemas[a.DataSchema]; ok {
		for pointer := range s.Properties {
			allowed[DataArgument(pointer)] = true
		}
	}
	if a.Meter != nil {
		allowed["meter_quantity"], allowed["meter_unit"], allowed["meter_name"] = true, true, true
	}
	return allowed
}

// MissingCategories reports the framework categories a profile requires that no
// catalogue emitting into it covers.
//
// This is the check that keeps a preset honest at deploy time: a profile may
// claim to satisfy a framework only if something in the installation actually
// records the events that framework asks for.
func MissingCategories(profile string, required []string, catalogues []*Catalogue) []string {
	covered := map[string]bool{}
	for _, c := range catalogues {
		for _, a := range c.Actions {
			if !contains(a.Profiles, profile) {
				continue
			}
			for _, cat := range a.Categories {
				covered[cat] = true
			}
		}
	}
	var missing []string
	for _, want := range required {
		if !covered[want] {
			missing = append(missing, want)
		}
	}
	sort.Strings(missing)
	return missing
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
