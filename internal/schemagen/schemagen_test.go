package schemagen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/internal/schemagen"
	"github.com/truvity/audit/record"
)

// The published schema carries the proto's comments. Without them an archived
// record would be structurally described and semantically mute, for exactly the
// reader the archive is kept for.
func TestPublishedSchemaCarriesTheProtoComments(t *testing.T) {
	var s struct {
		ID         string                     `json:"$id"`
		Comment    string                     `json:"$comment"`
		Desc       string                     `json:"description"`
		Properties map[string]json.RawMessage `json:"properties"`
		Defs       map[string]struct {
			Desc string `json:"description"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(published(t), &s); err != nil {
		t.Fatal(err)
	}
	if s.ID != schemagen.ID {
		t.Fatalf("$id = %q, want %q", s.ID, schemagen.ID)
	}
	if !strings.HasPrefix(s.Desc, "Record is one thing that happened") {
		t.Fatalf("the root description is not the proto's: %q", s.Desc)
	}
	if !strings.Contains(s.Comment, "archived beside it") {
		t.Fatalf("the generator's note is not in $comment: %q", s.Comment)
	}
	for _, field := range []string{"id", "actor", "tenant_id", "meter", "origin_hash"} {
		var p struct {
			Desc string `json:"description"`
		}
		if err := json.Unmarshal(s.Properties[field], &p); err != nil {
			t.Fatal(err)
		}
		if p.Desc == "" {
			t.Errorf("field %s has no description; the proto comment did not reach the schema", field)
		}
	}
	for _, def := range []string{"Actor", "Outcome", "Meter"} {
		if s.Defs[def].Desc == "" {
			t.Errorf("$defs.%s has no description", def)
		}
	}
}

// Every record in the corpus must parse as protobuf, satisfy the emitter's own
// rules, and validate against the published schema. The two descriptions of
// the record are held to each other here, which is the only place they meet.
func TestCorpusParsesAndValidates(t *testing.T) {
	schema := compiled(t)
	for _, path := range corpus(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var r record.Record
			if err := record.Unmarshal(raw, &r); err != nil {
				t.Fatalf("does not parse as a record: %v", err)
			}
			// The corpus is what the system accepts, not merely what it can
			// parse: a sample the emitter would refuse teaches the wrong thing
			// to everyone who reads it.
			if err := record.Check(&r, record.Default); err != nil {
				t.Fatalf("the corpus carries a record the emitter would refuse: %v", err)
			}
			validate(t, schema, raw, "the corpus file")

			// What the record package writes must itself satisfy the schema,
			// which is what makes the schema a description of this project's
			// output rather than of the corpus.
			canonical, err := record.Canonical(&r)
			if err != nil {
				t.Fatal(err)
			}
			validate(t, schema, canonical, "the canonical form")

			// Reading a record back and writing it again must produce the same
			// bytes, or an archived object could not be verified after a
			// round trip through any reader.
			var again record.Record
			if err := record.Unmarshal(canonical, &again); err != nil {
				t.Fatalf("the canonical form does not parse: %v", err)
			}
			twice, err := record.Canonical(&again)
			if err != nil {
				t.Fatal(err)
			}
			if string(twice) != string(canonical) {
				t.Fatalf("a round trip changed the record:\n%s\n%s", canonical, twice)
			}
		})
	}
}

// A record with a field the schema does not know must be refused, or the
// schema would not be describing a closed format.
func TestSchemaRefusesAnUnknownField(t *testing.T) {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(
		`{"id":"x","occurred_at":"2026-09-17T09:00:00Z","invented":"nonsense"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled(t).Validate(doc); err == nil {
		t.Fatal("an unknown field must be refused")
	}
}

// 64-bit integers are written as strings, because a JSON number cannot hold
// them all exactly. A reader that expects a number would silently lose the
// end of a sequence.
func TestSchemaWritesLargeIntegersAsStrings(t *testing.T) {
	asNumber, err := jsonschema.UnmarshalJSON(strings.NewReader(`{"id":"x","sequence":42}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled(t).Validate(asNumber); err == nil {
		t.Fatal("a sequence written as a number must be refused")
	}
	asString, err := jsonschema.UnmarshalJSON(strings.NewReader(`{"id":"x","sequence":"42"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled(t).Validate(asString); err != nil {
		t.Fatalf("a sequence written as a string must be accepted: %v", err)
	}
}

// The generator itself, without a plugin: comments given are carried, and the
// message's own comment is the description while the note goes to $comment.
func TestGenerateCarriesComments(t *testing.T) {
	md := (&auditv1.Record{}).ProtoReflect().Descriptor()
	comments := schemagen.Comments{
		md.FullName():                                    "Record is one thing.",
		md.Fields().ByName("id").FullName():              "Identity of the record.",
		md.Fields().ByName("actor").Message().FullName(): "Actor is who acted.",
	}
	b, err := schemagen.Generate(md, "urn:test", "t", "a note", comments)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"description": "Record is one thing."`,
		`"description": "Identity of the record."`,
		`"description": "Actor is who acted."`,
		`"$comment": "a note"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("generated schema lacks %s", want)
		}
	}
	if schemagen.Flatten("  a\n  b  \n\n c ") != "a b  c" {
		t.Fatalf("Flatten = %q", schemagen.Flatten("  a\n  b  \n\n c "))
	}
}

func validate(t *testing.T, s *jsonschema.Schema, raw []byte, what string) {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(doc); err != nil {
		t.Fatalf("%s does not satisfy the published schema: %v\n%s", what, err, raw)
	}
}

func published(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), schemagen.OutDir, schemagen.FileName))
	if err != nil {
		t.Fatalf("%v\n\nrun: just generate", err)
	}
	return raw
}

func compiled(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(published(t))))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemagen.ID, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(schemagen.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func corpus(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(repoRoot(t), "testdata", "records", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("the corpus is empty")
	}
	return paths
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("cannot find the repository root")
	return ""
}
