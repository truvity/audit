package schemagen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/truvity/audit/internal/schemagen"
	"github.com/truvity/audit/record"
)

// The generated schema on disk must be what the proto now says. A schema that
// has drifted from the record is worse than none: it would tell a reader in a
// later year that a valid record is invalid.
func TestGeneratedSchemaIsCurrent(t *testing.T) {
	want, err := schemagen.Record()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repoRoot(t), schemagen.OutDir, schemagen.FileName)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v\n\nrun: go run ./cmd/audit schema", err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is not what the proto says\n\nrun: go run ./cmd/audit schema", path)
	}
}

// Every record in the corpus must parse as protobuf and validate against the
// generated schema. The two descriptions of the record are held to each other
// here, which is the only place they meet.
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

			doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(doc); err != nil {
				t.Fatalf("does not satisfy the published schema: %v", err)
			}

			// What the record package writes must itself satisfy the schema,
			// which is what makes the schema a description of this project's
			// output rather than of the corpus.
			canonical, err := record.Canonical(&r)
			if err != nil {
				t.Fatal(err)
			}
			doc, err = jsonschema.UnmarshalJSON(strings.NewReader(string(canonical)))
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(doc); err != nil {
				t.Fatalf("the canonical form does not satisfy the published schema: %v\n%s", err, canonical)
			}

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
	asNumber, err := jsonschema.UnmarshalJSON(strings.NewReader(
		`{"id":"x","sequence":42}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled(t).Validate(asNumber); err == nil {
		t.Fatal("a sequence written as a number must be refused")
	}
	asString, err := jsonschema.UnmarshalJSON(strings.NewReader(
		`{"id":"x","sequence":"42"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled(t).Validate(asString); err != nil {
		t.Fatalf("a sequence written as a string must be accepted: %v", err)
	}
}

func compiled(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := schemagen.Record()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
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
