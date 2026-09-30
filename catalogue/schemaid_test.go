package catalogue

import (
	"strings"
	"testing"
)

func TestCanonicalID(t *testing.T) {
	for in, want := range map[string]string{
		"https://schemas.truvity.com/audit/v1/common/digest-written.json": SchemaBase + "common/digest-written.json",
		SchemaBase + "common/digest-written.json":                         SchemaBase + "common/digest-written.json",
		"https://schemas.example/wallet/credential-issued.json":           "https://schemas.example/wallet/credential-issued.json",
	} {
		if got := CanonicalID(in); got != want {
			t.Errorf("CanonicalID(%q) = %q, want %q", in, got, want)
		}
	}
}

// A catalogue archived under the old identifiers still loads: the reference
// and the schema's own $id are both the legacy form.
func TestLegacyIDsStillLoad(t *testing.T) {
	c, err := Common()
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.ReplaceAll(string(c.Document()), SchemaBase, legacySchemaBase)
	var schemas [][]byte
	for _, raw := range c.Schemas() {
		schemas = append(schemas, []byte(strings.ReplaceAll(string(raw), SchemaBase, legacySchemaBase)))
	}
	old, err := Load([]byte(doc), schemas)
	if err != nil {
		t.Fatalf("legacy catalogue: %v", err)
	}
	if len(old.Schemas()) != len(c.Schemas()) {
		t.Fatalf("legacy catalogue has %d schemas, current %d", len(old.Schemas()), len(c.Schemas()))
	}
	for id := range old.Schemas() {
		if !strings.HasPrefix(id, SchemaBase) {
			t.Errorf("schema keyed %q, want the canonical base", id)
		}
	}
	// Mixed: a new-form reference finds a schema that still says the old $id.
	mixed := [][]byte{}
	for _, raw := range c.Schemas() {
		mixed = append(mixed, []byte(strings.ReplaceAll(string(raw), SchemaBase, legacySchemaBase)))
	}
	if _, err := Load(c.Document(), mixed); err != nil {
		t.Fatalf("new references, legacy $id: %v", err)
	}
}
