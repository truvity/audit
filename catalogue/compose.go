package catalogue

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
)

// Composed is one action resolved against its catalogue: the schema of every
// slot a record of that action may fill. It is what an emitter validates
// against before publishing and what the writer validates against before
// writing.
type Composed struct {
	Source  string
	Version string
	Name    string
	Action  Action
	Data    *Schema

	catalogue *Catalogue
}

// Compose resolves an action.
func (c *Catalogue) Compose(action string) (*Composed, error) {
	a, ok := c.Actions[action]
	if !ok {
		return nil, fmt.Errorf("catalogue %s %s declares no action %s", c.Source, c.Version, action)
	}
	x := &Composed{Source: c.Source, Version: c.Version, Name: action, Action: a, catalogue: c}
	if a.DataSchema != "" {
		if s, ok := c.schemas[a.DataSchema]; ok {
			x.Data = s
		}
	}
	return x, nil
}

// Expiry is when the credential or certificate a record is about expires, read
// from whichever data properties the catalogue marks with x-audit-expiry — the
// latest of them when there are several, since the record is evidence for as
// long as the longest-lived thing relying on it. Nil means the record does not
// say, and an after_expiry profile falls back.
//
// It reads the record as written, before any profile's copy drops the data
// slot: the expiry decides how long a copy is kept even when the copy itself
// carries none of the data.
func (x *Composed) Expiry(r *record.Record) (*time.Time, error) {
	if x.Data == nil || r.GetData() == nil {
		return nil, nil
	}
	var latest *time.Time
	for pointer, p := range x.Data.Properties {
		if !p.Expiry {
			continue
		}
		text, ok := dataAt(r, pointer).(string)
		if !ok || text == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, text)
		if err != nil {
			return nil, fmt.Errorf("data%s is marked as the expiry and is not an RFC 3339 time: %q", pointer, text)
		}
		at = at.UTC()
		if latest == nil || at.After(*latest) {
			latest = &at
		}
	}
	return latest, nil
}

// Extends is the identifiers of the earlier records this one is an addendum
// to, read from the property the action's extends names. Nil means the action
// is no addendum, or this record names none.
func (x *Composed) Extends(r *record.Record) ([]string, error) {
	if x.Action.Extends == "" || r.GetData() == nil {
		return nil, nil
	}
	var ids []string
	switch v := dataAt(r, x.Action.Extends).(type) {
	case nil:
	case string:
		ids = append(ids, v)
	case []any:
		for _, item := range v {
			id, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("data%s names the records this one extends and holds a %T among them",
					x.Action.Extends, item)
			}
			ids = append(ids, id)
		}
	default:
		return nil, fmt.Errorf("data%s names the records this one extends and is a %T", x.Action.Extends, v)
	}
	out := ids[:0]
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// dataAt is the value at a pointer into a record's data slot, or nil.
func dataAt(r *record.Record, pointer string) any {
	var value any = r.GetData().AsMap()
	for _, step := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		m, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = m[step]
	}
	return value
}

// Validate holds a record to what its catalogue says it may be. An emitter runs
// it before publishing and the writer runs it again before writing, because the
// writer cannot take an emitter's word for what a record contains.
func (x *Composed) Validate(r *record.Record) error {
	var problems []error
	fail := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }

	if r.GetSource() != x.Source {
		fail("record names source %q but was validated against the catalogue of %q", r.GetSource(), x.Source)
	}
	if r.GetCatalogueVersion() != x.Version {
		fail("record names catalogue version %q but was validated against %q", r.GetCatalogueVersion(), x.Version)
	}
	if r.GetAction() != x.Name {
		fail("record names action %q but was validated against %q", r.GetAction(), x.Name)
	}
	if want, ok := operationOf(x.Action.Operation); ok && r.GetOperation() != want {
		fail("action %s is declared as %s, but the record says %s",
			x.Name, x.Action.Operation, strings.ToLower(strings.TrimPrefix(r.GetOperation().String(), "OPERATION_")))
	}
	if level, ok := captureOf(x.Action.CaptureLevel); ok && r.GetCapture().GetLevel() > level {
		fail("action %s may capture at most %s, but the record captures more", x.Name, x.Action.CaptureLevel)
	}

	problems = append(problems, x.validateData(r)...)
	problems = append(problems, x.validateActor(r)...)
	problems = append(problems, x.validateTargets(r)...)
	problems = append(problems, x.validateContext(r)...)
	problems = append(problems, x.validateMeter(r)...)
	return errors.Join(problems...)
}

func (x *Composed) validateData(r *record.Record) []error {
	if x.Data == nil {
		if r.GetData() != nil && len(r.GetData().GetFields()) > 0 {
			return []error{fmt.Errorf("action %s declares no data schema, so it may carry no data", x.Name)}
		}
		return nil
	}
	if err := x.Data.Validate(asAny(r.GetData())); err != nil {
		return []error{fmt.Errorf("data: %w", err)}
	}
	return nil
}

func (x *Composed) validateActor(r *record.Record) []error {
	a := r.GetActor()
	if a == nil || a.GetKind() == "" {
		return nil
	}
	kind, ok := x.catalogue.ActorKinds[a.GetKind()]
	if !ok {
		return []error{fmt.Errorf("actor kind %q is not declared by catalogue %s", a.GetKind(), x.Source)}
	}
	if kind.AttributesSchema == "" {
		if len(a.GetAttributes().GetFields()) > 0 {
			return []error{fmt.Errorf("actor kind %q declares no attributes schema, so it may carry no attributes", a.GetKind())}
		}
		return nil
	}
	s, ok := x.catalogue.schemas[kind.AttributesSchema]
	if !ok {
		return []error{fmt.Errorf("actor kind %q references schema %s, which is not loaded", a.GetKind(), kind.AttributesSchema)}
	}
	if err := s.Validate(asAny(a.GetAttributes())); err != nil {
		return []error{fmt.Errorf("actor.attributes: %w", err)}
	}
	return nil
}

func (x *Composed) validateTargets(r *record.Record) []error {
	var problems []error
	allowed := map[string]bool{}
	for _, t := range x.Action.TargetTypes {
		allowed[t] = true
	}
	for i, t := range r.GetTargets() {
		declared, ok := x.catalogue.TargetTypes[t.GetType()]
		if !ok {
			problems = append(problems, fmt.Errorf("targets[%d]: type %q is not declared by catalogue %s", i, t.GetType(), x.Source))
			continue
		}
		if len(allowed) > 0 && !allowed[t.GetType()] {
			problems = append(problems, fmt.Errorf("targets[%d]: action %s is not declared to act on %q", i, x.Name, t.GetType()))
		}
		// A person is named by identifier, never by display name: a name in a
		// target is an identity attribute, and the record carries none.
		if declared.IsPerson && t.GetName() != "" {
			problems = append(problems, fmt.Errorf("targets[%d]: target type %q is a person, so it may carry no name", i, t.GetType()))
		}
		if declared.AttributesSchema == "" {
			if len(t.GetAttributes().GetFields()) > 0 {
				problems = append(problems, fmt.Errorf("targets[%d]: type %q declares no attributes schema", i, t.GetType()))
			}
			continue
		}
		s, ok := x.catalogue.schemas[declared.AttributesSchema]
		if !ok {
			problems = append(problems, fmt.Errorf("targets[%d]: schema %s is not loaded", i, declared.AttributesSchema))
			continue
		}
		if err := s.Validate(asAny(t.GetAttributes())); err != nil {
			problems = append(problems, fmt.Errorf("targets[%d].attributes: %w", i, err))
		}
	}
	return problems
}

func (x *Composed) validateContext(r *record.Record) []error {
	var problems []error
	for area, value := range r.GetContext().GetAreas() {
		id, ok := x.catalogue.ContextAreas[area]
		if !ok {
			problems = append(problems, fmt.Errorf("context area %q is not declared by catalogue %s", area, x.Source))
			continue
		}
		s, ok := x.catalogue.schemas[id]
		if !ok {
			problems = append(problems, fmt.Errorf("context area %q references schema %s, which is not loaded", area, id))
			continue
		}
		if err := s.Validate(asAny(value)); err != nil {
			problems = append(problems, fmt.Errorf("context.areas.%s: %w", area, err))
		}
	}
	return problems
}

func (x *Composed) validateMeter(r *record.Record) []error {
	declared := x.Action.Meter
	carried := r.GetMeter()
	switch {
	case declared == nil && carried == nil:
		return nil
	case declared == nil:
		return []error{fmt.Errorf("action %s is not metered, so it may carry no meter", x.Name)}
	case carried == nil:
		return []error{fmt.Errorf("action %s meters %q, so every record of it must carry the measurement", x.Name, declared.Name)}
	}

	var problems []error
	m := x.catalogue.Meters[declared.Name]
	if carried.GetName() != declared.Name {
		problems = append(problems, fmt.Errorf("meter.name is %q, but action %s meters %q", carried.GetName(), x.Name, declared.Name))
	}
	if m.Unit != "" && carried.GetUnit() != m.Unit {
		problems = append(problems, fmt.Errorf("meter %q is measured in %q, but the record says %q", declared.Name, m.Unit, carried.GetUnit()))
	}
	if want, ok := meterKindOf(m.Kind); ok && carried.GetKind() != want {
		problems = append(problems, fmt.Errorf("meter %q is a %s", declared.Name, m.Kind))
	}
	if m.DimensionsSchema != "" {
		if s, ok := x.catalogue.schemas[m.DimensionsSchema]; ok {
			if err := s.Validate(asAny(carried.GetDimensions())); err != nil {
				problems = append(problems, fmt.Errorf("meter.dimensions: %w", err))
			}
		}
	} else if len(carried.GetDimensions().GetFields()) > 0 {
		problems = append(problems, fmt.Errorf("meter %q declares no dimensions schema", declared.Name))
	}
	return problems
}

func asAny(s *structpb.Struct) any {
	if s == nil {
		return map[string]any{}
	}
	return s.AsMap()
}

func operationOf(name string) (auditv1.Operation, bool) {
	v, ok := auditv1.Operation_value["OPERATION_"+strings.ToUpper(name)]
	return auditv1.Operation(v), ok && name != ""
}

func captureOf(name string) (auditv1.Capture_Level, bool) {
	v, ok := auditv1.Capture_Level_value["LEVEL_"+strings.ToUpper(name)]
	return auditv1.Capture_Level(v), ok && name != ""
}

func meterKindOf(name string) (auditv1.Meter_Kind, bool) {
	v, ok := auditv1.Meter_Kind_value["KIND_"+strings.ToUpper(name)]
	return auditv1.Meter_Kind(v), ok && name != ""
}

// Category is the actor category a kind belongs to, which is what a profile's
// identity treatment follows. An unknown kind is treated as external, the
// strictest of the three, because a kind nobody declared is not one to take
// chances with.
func (x *Composed) Category(kind string) preset.Category {
	if k, ok := x.catalogue.ActorKinds[kind]; ok && k.Category != "" {
		return k.Category
	}
	return preset.External
}

// TargetIsPerson reports whether a target type names a person, whose identifier
// is treated like any other person's.
func (x *Composed) TargetIsPerson(targetType string) bool {
	return x.catalogue.TargetTypes[targetType].IsPerson
}
