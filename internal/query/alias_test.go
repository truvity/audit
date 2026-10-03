package query_test

import (
	"context"
	"testing"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/internal/query"
	"github.com/truvity/audit/sdk/catalogue"
	auditv1 "github.com/truvity/audit/sdk/gen/audit/v1"
)

func renamed(t *testing.T) catalogue.Names {
	t.Helper()
	c, err := catalogue.Load([]byte(`
source: sluis
aliases: [access-roster]
version: "2.0.0"
locales: [en]
actions:
  sluis.grant.issued:
    summary: A grant was issued.
    operation: create
    categories: [data_change]
    profiles: [security]
    message: { en: "{actor} issued a grant" }
`), nil)
	if err != nil {
		t.Fatal(err)
	}
	return catalogue.Names{c}
}

// A filter by the former name of a source reaches what the index holds under
// the current one, and a filter by the current one is unchanged.
func TestAFilterByAFormerSourceNameFindsTheCurrentOne(t *testing.T) {
	names := renamed(t)
	m := index.NewMemory()
	rows := []index.Row{
		{ID: "a", TenantID: "acme", Source: "sluis", Action: "sluis.grant.issued", ObjectKey: "k", Line: 1},
		{ID: "b", TenantID: "acme", Source: "other", Action: "other.thing", ObjectKey: "k", Line: 2},
	}
	if err := m.Index(context.Background(), "security", rows); err != nil {
		t.Fatal(err)
	}

	for name, f := range map[string]*auditv1.Filter{
		"source, former name":  {Source: eq("access-roster")},
		"source, current name": {Source: eq("sluis")},
		"source in, mixed": {Source: &auditv1.StringPredicate{Operator: &auditv1.StringPredicate_In{
			In: &auditv1.StringList{Values: []string{"access-roster", "sluis"}}}}},
		"action, former name":   {Action: eq("access-roster.grant.issued")},
		"action prefix, former": {Action: &auditv1.StringPredicate{Operator: &auditv1.StringPredicate_Prefix{Prefix: "access-roster."}}},
	} {
		q, err := query.CompileWith(&auditv1.SearchRequest{Profile: "security", Filter: []*auditv1.Filter{f}}, names)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		page, err := m.Search(context.Background(), q)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(page.Rows) != 1 || page.Rows[0].ID != "a" {
			t.Errorf("%s: found %d rows", name, len(page.Rows))
		}
	}

	// Without the names the former name finds nothing: the rewrite is the
	// whole of the feature on this side.
	q, err := query.Compile(&auditv1.SearchRequest{Profile: "security", Filter: []*auditv1.Filter{
		{Source: eq("access-roster")}}})
	if err != nil {
		t.Fatal(err)
	}
	if page, _ := m.Search(context.Background(), q); len(page.Rows) != 0 {
		t.Fatalf("an unrewritten filter found %d rows", len(page.Rows))
	}
}

func eq(v string) *auditv1.StringPredicate {
	return &auditv1.StringPredicate{Operator: &auditv1.StringPredicate_Equal{Equal: v}}
}
