package registry_test

import (
	"context"
	"testing"

	"github.com/truvity/audit/internal/pgtest"
	"github.com/truvity/audit/internal/registry"
)

// The registry shares the index's database and its migration chain, so the
// table has to be there after the same migrate the writer runs.
func TestPostgresKeepsACatalogue(t *testing.T) {
	pool := pgtest.Open(t)
	store := registry.Postgres{DB: pool}
	r := &registry.Registry{
		Store:    store,
		Identity: func(context.Context) string { return "wallet" },
	}
	ctx := context.Background()

	// A catalogue with an extension slot, and the schema it references. Both
	// halves are checked: a schema nothing references is refused, and so is a
	// reference to a schema nobody supplied.
	doc := `source: wallet
version: "1.0.0"
locales: [en]
actions:
  wallet.credential.issued:
    summary: A credential was issued.
    operation: create
    categories: [data_change]
    profiles: [security]
    data_schema: https://example.test/s.json
    message: { en: "{actor} issued a credential" }
`
	schema := `{
  "$id": "https://example.test/s.json",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "credential_type": {
      "type": "string",
      "x-audit-class": "shared",
      "x-audit-pii": "none"
    }
  }
}`
	e := registry.Entry{
		Source: "wallet", Version: "1.0.0", Document: []byte(doc),
		Schemas: map[string][]byte{"https://example.test/s.json": []byte(schema)},
	}
	problems, err := r.Register(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}

	got, err := store.Get(ctx, "wallet", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Document) != doc {
		t.Fatal("the document came back changed")
	}
	if string(got.Schemas["https://example.test/s.json"]) != schema {
		t.Fatalf("the schemas came back as %v", got.Schemas)
	}
	if got.RegisteredBy != "wallet" || got.RegisteredAt.IsZero() {
		t.Fatalf("who and when were not kept: %+v", got)
	}

	all, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("%d entries", len(all))
	}
}

// Registering twice is how a deployment rolls; the second must not fail on the
// primary key.
func TestPostgresTakesTheSameCatalogueTwice(t *testing.T) {
	pool := pgtest.Open(t)
	r := &registry.Registry{
		Store:    registry.Postgres{DB: pool},
		Identity: func(context.Context) string { return "wallet" },
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		problems, err := r.Register(ctx, entry(walletDoc))
		if err != nil || len(problems) != 0 {
			t.Fatalf("pass %d: %v %v", i, problems, err)
		}
	}
}
