// Package writer turns records into the copies the profiles keep, and puts
// those copies where they are kept.
package writer

import (
	"context"
	"fmt"
	"sort"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
)

// clearField removes one core field from a copy. The map is written out rather
// than derived, because what a copy carries is the thing this system is most
// obliged to get right, and a reviewer should be able to read it.
var clearField = map[string]func(*record.Record){
	"/id":                  func(r *record.Record) { r.Id = "" },
	"/occurred_at":         func(r *record.Record) { r.OccurredAt = nil },
	"/recorded_at":         func(r *record.Record) { r.RecordedAt = nil },
	"/schema_version":      func(r *record.Record) { r.SchemaVersion = "" },
	"/catalogue_version":   func(r *record.Record) { r.CatalogueVersion = "" },
	"/source":              func(r *record.Record) { r.Source = "" },
	"/observer":            func(r *record.Record) { r.Observer = nil },
	"/sequence":            func(r *record.Record) { r.Sequence = 0 },
	"/action":              func(r *record.Record) { r.Action = "" },
	"/operation":           func(r *record.Record) { r.Operation = 0 },
	"/outcome":             func(r *record.Record) { r.Outcome = nil },
	"/tenant_id":           func(r *record.Record) { r.TenantId = "" },
	"/subject":             func(r *record.Record) { r.Subject = nil },
	"/actor":               func(r *record.Record) { r.Actor = nil },
	"/targets":             func(r *record.Record) { r.Targets = nil },
	"/context":             func(r *record.Record) { r.Context = nil },
	"/capture":             func(r *record.Record) { r.Capture = nil },
	"/previous_attributes": func(r *record.Record) { r.PreviousAttributes = nil },
	"/data":                func(r *record.Record) { r.Data = nil },
	"/meter":               func(r *record.Record) { r.Meter = nil },
	"/attributes":          func(r *record.Record) { r.Attributes = nil },
	"/unmapped":            func(r *record.Record) { r.Unmapped = nil },
	"/origin_hash":         func(r *record.Record) { r.OriginHash = "" },
}

// CoreFields is every core field a profile may name, in order. Exported so that
// a validator can tell a preset that names /acotr it has made a typo, rather
// than silently keeping nothing.
func CoreFields() []string {
	out := make([]string, 0, len(clearField))
	for f := range clearField {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// Splitter turns one record into the copies its action's profiles keep.
type Splitter struct {
	// Profiles the deployment has, by name.
	Profiles map[string]*preset.Profile
	// Keys gives the pseudonyms. A splitter without one cannot produce a copy
	// for a profile that pseudonymises, and says so rather than writing the
	// identifier in clear.
	Keys keys.Provider
}

// Split returns one copy per profile the action belongs to and the deployment
// has, in a stable order.
//
// A copy is default-deny: every core field a preset does not name is removed,
// and an extension property survives only if its class is one the profile keeps
// and its PII level is not one the profile refuses. Adding a field to the
// record therefore cannot quietly widen a copy of it.
func (s *Splitter) Split(ctx context.Context, r *record.Record, x *catalogue.Composed) ([]*record.Record, error) {
	names := make([]string, 0, len(x.Action.Profiles))
	for _, name := range x.Action.Profiles {
		if _, ok := s.Profiles[name]; ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	copies := make([]*record.Record, 0, len(names))
	for _, name := range names {
		c, err := s.copyFor(ctx, r, x, s.Profiles[name])
		if err != nil {
			return nil, err
		}
		copies = append(copies, c)
	}
	return copies, nil
}

// Unhandled returns the profiles an action names that this deployment does not
// have. A record whose action belongs to a profile nobody configured is not an
// error, but it is a fact an operator should be told once rather than never.
func (s *Splitter) Unhandled(x *catalogue.Composed) []string {
	var out []string
	for _, name := range x.Action.Profiles {
		if _, ok := s.Profiles[name]; !ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Splitter) copyFor(
	ctx context.Context, r *record.Record, x *catalogue.Composed, p *preset.Profile,
) (*record.Record, error) {
	c := proto.Clone(r).(*record.Record)
	c.Profile = p.Name

	for _, field := range CoreFields() {
		if field == "/origin_hash" && !p.KeepsField(field) {
			// Without it, two copies of one event cannot be shown to descend
			// from the same original, which is the only thing that relates
			// them once the identifiers differ. A preset may not drop it.
			continue
		}
		if !p.KeepsField(field) {
			clearField[field](c)
		}
	}

	if err := s.treatIdentities(ctx, c, x, p); err != nil {
		return nil, err
	}
	if err := s.filterSlots(ctx, c, x, p); err != nil {
		return nil, err
	}
	return c, nil
}

// treatIdentities applies the profile's treatment to whoever the record names.
//
// The treatment follows the actor kind's category, never a field name, so a
// source that adds a kind cannot widen what is kept by choosing a name.
func (s *Splitter) treatIdentities(
	ctx context.Context, c *record.Record, x *catalogue.Composed, p *preset.Profile,
) error {
	tenant := c.GetTenantId()
	if tenant == "" {
		tenant = record.TenantPlatform
	}

	if a := c.GetActor(); a != nil {
		id, err := s.treat(ctx, tenant, p, x.Category(a.GetKind()), a.GetId())
		if err != nil {
			return fmt.Errorf("actor: %w", err)
		}
		a.Id = id
		// A session identifier locates a person as surely as their name does,
		// so it follows the same treatment.
		if a.GetSessionId() != "" {
			session, err := s.treat(ctx, tenant, p, x.Category(a.GetKind()), a.GetSessionId())
			if err != nil {
				return fmt.Errorf("actor.session_id: %w", err)
			}
			a.SessionId = session
		}
	}
	if subject := c.GetSubject(); subject != nil {
		id, err := s.treat(ctx, tenant, p, x.Category(subject.GetKind()), subject.GetId())
		if err != nil {
			return fmt.Errorf("subject: %w", err)
		}
		subject.Id = id
	}
	for i, t := range c.GetTargets() {
		if !x.TargetIsPerson(t.GetType()) {
			continue
		}
		id, err := s.treat(ctx, tenant, p, preset.External, t.GetId())
		if err != nil {
			return fmt.Errorf("targets[%d]: %w", i, err)
		}
		t.Id = id
	}
	return nil
}

// treat applies one treatment to one identifier.
func (s *Splitter) treat(
	ctx context.Context, tenant string, p *preset.Profile, category preset.Category, id string,
) (string, error) {
	if id == "" {
		return "", nil
	}
	switch p.Identity[category] {
	case preset.Omit:
		return "", nil
	case preset.Pseudonym:
		if s.Keys == nil {
			return "", fmt.Errorf(
				"profile %s pseudonymises %s identifiers and no key provider is configured", p.Name, category)
		}
		// The purpose is the profile, so that two copies of one event carry
		// different pseudonyms for the same person and cannot be joined.
		return s.Keys.Pseudonym(ctx, tenant, keys.Purpose(p.Name), id)
	default:
		// Clear and scoped both keep the identifier. They differ in who may
		// read the copy, which is the profile's access rule and not the
		// writer's to enforce here.
		return id, nil
	}
}

// filterSlots removes from every extension slot what the profile does not keep,
// and treats what it keeps but must not leave readable.
func (s *Splitter) filterSlots(
	ctx context.Context, c *record.Record, x *catalogue.Composed, p *preset.Profile,
) error {
	tenant := c.GetTenantId()
	if tenant == "" {
		tenant = record.TenantPlatform
	}
	if c.GetData() != nil && x.Data != nil {
		kept, err := s.filter(ctx, tenant, p, x.Data, c.GetData(), "")
		if err != nil {
			return fmt.Errorf("data: %w", err)
		}
		c.Data = kept
	} else if c.GetData() != nil {
		// No schema means no annotations, and an unannotated property has no
		// account of what it is. It is kept only where the bags are.
		if !p.KeepsProperty(preset.Audit, "none") {
			c.Data = nil
		}
	}
	// The bags carry no annotations at all, so they follow the audit class.
	if !p.KeepsProperty(preset.Audit, "none") {
		c.Attributes = nil
		c.Unmapped = nil
	}
	return nil
}

// filter walks one slot, keeping what the profile keeps.
func (s *Splitter) filter(
	ctx context.Context, tenant string, p *preset.Profile,
	schema *catalogue.Schema, value *structpb.Struct, prefix string,
) (*structpb.Struct, error) {
	if value == nil {
		return nil, nil
	}
	out := &structpb.Struct{Fields: map[string]*structpb.Value{}}
	for name, v := range value.GetFields() {
		pointer := prefix + "/" + name
		property, known := schema.Properties[pointer]
		if !known {
			// A property the schema does not describe cannot be judged, and
			// what cannot be judged is not kept.
			continue
		}
		if !p.KeepsProperty(property.Class, property.PII) {
			continue
		}
		switch property.Sensitive {
		case "redact":
			continue
		case "hmac":
			if s.Keys == nil {
				return nil, fmt.Errorf("%s must be hashed and no key provider is configured", pointer)
			}
			hashed, err := s.Keys.Pseudonym(ctx, tenant, keys.Purpose(p.Name), v.GetStringValue())
			if err != nil {
				return nil, err
			}
			out.Fields[name] = structpb.NewStringValue(hashed)
			continue
		}
		if nested := v.GetStructValue(); nested != nil {
			kept, err := s.filter(ctx, tenant, p, schema, nested, pointer)
			if err != nil {
				return nil, err
			}
			out.Fields[name] = structpb.NewStructValue(kept)
			continue
		}
		out.Fields[name] = v
	}
	if len(out.GetFields()) == 0 {
		return nil, nil
	}
	return out, nil
}
