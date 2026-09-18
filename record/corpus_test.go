package record_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
)

// The corpus is what keeps the two descriptions of a record honest with each
// other: the proto and the JSON Schema generated from it. Every example must
// be accepted by both, strictly — an unknown field fails the proto side — and
// together the examples must use every field a record can carry and every
// value of every enum, so that a field the schema generator renders wrongly
// cannot hide behind the corpus never mentioning it.
//
// For a while nothing read these files at all. A corpus nobody checks is a
// folder of JSON that happens to be nearby.
func TestTheCorpusIsAcceptedByBothDescriptionsAndCoversTheRecord(t *testing.T) {
	schema := recordSchema(t)
	files, err := filepath.Glob("../testdata/records/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no corpus")
	}

	fields, values := map[string]bool{}, map[string]bool{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var r auditv1.Record
		if err := (protojson.UnmarshalOptions{}).Unmarshal(raw, &r); err != nil {
			t.Errorf("%s: not a record the proto accepts: %v", filepath.Base(file), err)
			continue
		}
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); err != nil {
			t.Errorf("%s: not a record the JSON Schema accepts: %v", filepath.Base(file), err)
		}
		used(r.ProtoReflect(), fields, values)
	}

	var missing []string
	for _, f := range reachable(t) {
		if !fields[f] {
			missing = append(missing, "field "+f)
		}
	}
	for _, v := range enumValues() {
		if !values[v] {
			missing = append(missing, "value "+v)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("the corpus never uses %d thing(s) a record can carry:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

func recordSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile("../gen/jsonschema/record.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var head struct {
		ID string `json:"$id"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		t.Fatal(err)
	}
	id := head.ID
	if id == "" {
		id = "record.v1.schema.json"
	}
	parsed, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(id, parsed); err != nil {
		t.Fatal(err)
	}
	compiled, err := c.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// ours is whether a message is this repository's rather than a well-known
// type, whose own fields are protobuf's business.
func ours(m protoreflect.MessageDescriptor) bool {
	return strings.HasPrefix(string(m.FullName()), "audit.")
}

// reachable is every field of every message a Record can contain.
func reachable(t *testing.T) []string {
	t.Helper()
	var out []string
	seen := map[protoreflect.FullName]bool{}
	var walk func(protoreflect.MessageDescriptor)
	walk = func(m protoreflect.MessageDescriptor) {
		if seen[m.FullName()] || !ours(m) {
			return
		}
		seen[m.FullName()] = true
		fs := m.Fields()
		for i := 0; i < fs.Len(); i++ {
			f := fs.Get(i)
			out = append(out, string(f.FullName()))
			switch {
			case f.IsMap():
				// A map's entry is a synthetic message whose key and value
				// are not fields anyone sets; the map is. What its values
				// contain still counts.
				if v := f.MapValue(); v.Message() != nil {
					walk(v.Message())
				}
			case f.Message() != nil:
				walk(f.Message())
			}
		}
	}
	walk((&auditv1.Record{}).ProtoReflect().Descriptor())
	return out
}

// enumValues is every value of every enum a Record can contain, less the
// UNSPECIFIED zero a well-formed record never carries.
func enumValues() []string {
	var out []string
	seen := map[protoreflect.FullName]bool{}
	var walk func(protoreflect.MessageDescriptor)
	walk = func(m protoreflect.MessageDescriptor) {
		if seen[m.FullName()] || !ours(m) {
			return
		}
		seen[m.FullName()] = true
		fs := m.Fields()
		for i := 0; i < fs.Len(); i++ {
			f := fs.Get(i)
			if e := f.Enum(); e != nil && !seen[e.FullName()] {
				seen[e.FullName()] = true
				vs := e.Values()
				for j := 0; j < vs.Len(); j++ {
					if vs.Get(j).Number() != 0 {
						out = append(out, string(vs.Get(j).FullName()))
					}
				}
			}
			if f.Message() != nil {
				walk(f.Message())
			}
		}
	}
	walk((&auditv1.Record{}).ProtoReflect().Descriptor())
	return out
}

// used marks every field set in a message, and every enum value it carries.
func used(m protoreflect.Message, fields, values map[string]bool) {
	if !ours(m.Descriptor()) {
		return
	}
	m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		fields[string(f.FullName())] = true
		switch {
		case f.IsList():
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				one(f, l.Get(i), fields, values)
			}
		case f.IsMap():
			v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				one(f.MapValue(), mv, fields, values)
				return true
			})
		default:
			one(f, v, fields, values)
		}
		return true
	})
}

func one(f protoreflect.FieldDescriptor, v protoreflect.Value, fields, values map[string]bool) {
	switch {
	case f.Enum() != nil:
		if d := f.Enum().Values().ByNumber(v.Enum()); d != nil {
			values[string(d.FullName())] = true
		}
	case f.Message() != nil:
		used(v.Message(), fields, values)
	}
}
