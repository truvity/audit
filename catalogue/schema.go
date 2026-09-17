package catalogue

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/truvity/audit/internal/metaschema"
	"github.com/truvity/audit/preset"
)

// Schema is one extension-slot schema, with the annotations the generic
// components read: which profiles a property survives into, whether it may be
// faceted or filtered, how a sensitive value is treated, and where it maps in
// an exported record.
type Schema struct {
	ID         string
	Raw        []byte
	Properties map[string]Property

	compiled *jsonschema.Schema
}

// Property is one annotated property of an extension schema, addressed by its
// JSON pointer within the slot.
type Property struct {
	Type      string
	Class     preset.Class
	PII       string
	Facet     bool
	Filter    bool
	Sensitive string
	OCSFPath  string
	ECSPath   string
}

// LoadSchema reads an extension-slot schema, holds it to the constraints every
// such schema must satisfy, and indexes its annotations.
func LoadSchema(raw []byte) (*Schema, error) {
	if err := metaschema.Validate("extension.schema.json", raw); err != nil {
		return nil, err
	}
	var doc struct {
		ID         string                     `json:"$id"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("extension schema: %w", err)
	}
	parsed, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(doc.ID, parsed); err != nil {
		return nil, err
	}
	compiled, err := c.Compile(doc.ID)
	if err != nil {
		return nil, fmt.Errorf("extension schema %s: %w", doc.ID, err)
	}
	s := &Schema{ID: doc.ID, Raw: raw, Properties: map[string]Property{}, compiled: compiled}
	for name, sub := range doc.Properties {
		if err := s.index("/"+name, sub, 1); err != nil {
			return nil, fmt.Errorf("extension schema %s: %w", doc.ID, err)
		}
	}
	return s, nil
}

// maxDepth is how deeply an extension property may nest. Three levels is
// enough for any structured fact and shallow enough that a facet, a filter or
// an export mapping can still name every property by a short pointer. A
// meta-schema cannot count, so the loader does.
const maxDepth = 3

// index records one property and walks into whatever it contains, so a nested
// object is annotated property by property rather than wholesale.
func (s *Schema) index(pointer string, raw json.RawMessage, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("%s: nested deeper than %d levels", pointer, maxDepth)
	}
	var p struct {
		Type       string                     `json:"type"`
		Class      preset.Class               `json:"x-audit-class"`
		PII        string                     `json:"x-audit-pii"`
		Facet      bool                       `json:"x-audit-facet"`
		Filter     bool                       `json:"x-audit-filter"`
		Sensitive  string                     `json:"x-audit-sensitive"`
		OCSFPath   string                     `json:"x-ocsf-path"`
		ECSPath    string                     `json:"x-ecs-path"`
		Properties map[string]json.RawMessage `json:"properties"`
		Items      json.RawMessage            `json:"items"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("%s: %w", pointer, err)
	}
	s.Properties[pointer] = Property{
		Type: p.Type, Class: p.Class, PII: p.PII, Facet: p.Facet,
		Filter: p.Filter, Sensitive: p.Sensitive, OCSFPath: p.OCSFPath, ECSPath: p.ECSPath,
	}
	for name, sub := range p.Properties {
		if err := s.index(pointer+"/"+name, sub, depth+1); err != nil {
			return err
		}
	}
	if len(p.Items) > 0 {
		var item struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(p.Items, &item); err == nil {
			for name, sub := range item.Properties {
				if err := s.index(pointer+"/"+name, sub, depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Validate checks a value against the schema. The value is the decoded JSON of
// one extension slot.
func (s *Schema) Validate(v any) error {
	if s == nil || s.compiled == nil {
		return nil
	}
	if err := s.compiled.Validate(v); err != nil {
		return fmt.Errorf("does not satisfy %s: %w", s.ID, err)
	}
	return nil
}

// Facets are the properties a searcher may count, which is what turns an
// application's own data into a facet in the viewer without any code.
func (s *Schema) Facets() []string { return s.selected(func(p Property) bool { return p.Facet }) }

// Filterable are the properties a query may name in a predicate.
func (s *Schema) Filterable() []string {
	return s.selected(func(p Property) bool { return p.Filter || p.Facet })
}

func (s *Schema) selected(keep func(Property) bool) []string {
	var out []string
	for pointer, p := range s.Properties {
		if keep(p) {
			out = append(out, pointer)
		}
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// validateAgainst is the package-local shorthand for a meta-schema check.
func validateAgainst(name string, document []byte) error { return metaschema.Validate(name, document) }
