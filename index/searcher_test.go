package index_test

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/audit/index"
)

// The memory searcher is not a convenience: it is the second implementation
// that keeps the interface from being a description of Postgres's habits. These
// tests state what any searcher must do, and the Postgres package runs the same
// questions against a real database.

func searchable(t *testing.T) *index.Memory {
	t.Helper()
	m := index.NewMemory()
	base := at(t, "2026-09-17T10:00:00Z")
	var rows []index.Row
	for n := 0; n < 6; n++ {
		// Offset into the hex alphabet so no identifier ends in twelve digits.
		r := index.RowOf(copied(t, "018f0000-0000-7000-8000-00000000000"+
			string("abcdef0123456789"[n])), index.ObjectAt{Key: "k", Line: n + 1}, fields)
		r.OccurredAt = base.Add(time.Duration(n) * time.Minute)
		r.RecordedAt = base.Add(time.Duration(n) * time.Minute)
		switch n % 3 {
		case 0:
			r.Action, r.Outcome = "wallet.credential.issued", "success"
		case 1:
			r.Action, r.Outcome = "wallet.credential.revoked", "success"
		case 2:
			r.Action, r.Outcome = "wallet.credential.issued", "failure"
		}
		if n >= 3 {
			r.TenantID = "globex"
		}
		rows = append(rows, r)
	}
	if err := m.Index(context.Background(), "security", rows); err != nil {
		t.Fatal(err)
	}
	return m
}

func searchIDs(t *testing.T, page index.Page) []string {
	t.Helper()
	out := make([]string, 0, len(page.Rows))
	for _, r := range page.Rows {
		out = append(out, r.ID)
	}
	return out
}

// The grant is a term in the query, not a layer above it.
func TestMemorySearchHonoursTheGrant(t *testing.T) {
	m := searchable(t)
	page, err := m.Search(context.Background(), index.Query{
		Profile: "security", Tenants: []string{"acme"}, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 3 {
		t.Fatalf("%d rows, want the 3 of the granted tenant", len(page.Rows))
	}
	for _, r := range page.Rows {
		if r.TenantID != "acme" {
			t.Fatalf("a row of %s came back under a grant for acme", r.TenantID)
		}
	}
}

// Keyset paging: every row once, in order, however small the pages.
func TestMemoryPagesWithoutRepeatingOrSkipping(t *testing.T) {
	m := searchable(t)
	ctx := context.Background()
	q := index.Query{Profile: "security", Limit: 2,
		Sort: []index.SortBy{{Field: index.SortOccurredAt}}}

	var seen []string
	for page := 0; page < 5; page++ {
		got, err := m.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, searchIDs(t, got)...)
		if !got.More {
			break
		}
		q.After = got.Next
	}
	if len(seen) != 6 {
		t.Fatalf("paged %d rows of 6: %v", len(seen), seen)
	}
	unique := map[string]bool{}
	for _, s := range seen {
		if unique[s] {
			t.Fatalf("a row came back on two pages: %v", seen)
		}
		unique[s] = true
	}
}

// The last page keeps its place, because a tail keeps polling it.
func TestMemoryTailResumesFromTheLastPage(t *testing.T) {
	m := searchable(t)
	ctx := context.Background()
	q := index.Query{Profile: "security", Limit: 100,
		Sort: []index.SortBy{{Field: index.SortRecordedAt}}}

	page, err := m.Search(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if page.Next == nil {
		t.Fatal("the last page has no boundary, so a tail cannot resume")
	}
	q.After = page.Next
	empty, err := m.Search(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Rows) != 0 || empty.Next == nil {
		t.Fatalf("an empty page lost its place: %d rows, next=%v", len(empty.Rows), empty.Next)
	}

	late := index.RowOf(copied(t, "018f0000-0000-7000-8000-0000000000ff"),
		index.ObjectAt{Key: "k", Line: 9}, fields)
	late.RecordedAt = at(t, "2026-09-17T11:00:00Z")
	late.OccurredAt = late.RecordedAt
	if err := m.Index(ctx, "security", []index.Row{late}); err != nil {
		t.Fatal(err)
	}
	q.After = empty.Next
	caught, err := m.Search(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(caught.Rows) != 1 {
		t.Fatalf("the tail missed a record recorded after the last page: %v", searchIDs(t, caught))
	}
}

func TestMemorySearchFilters(t *testing.T) {
	m := searchable(t)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		with index.Conjunction
		want int
	}{
		{"equal", index.Conjunction{
			Action: []index.Predicate{{Op: index.Equal, Value: "wallet.credential.issued"}}}, 4},
		{"not equal", index.Conjunction{
			Outcome: []index.Predicate{{Op: index.NotEqual, Value: "failure"}}}, 4},
		{"prefix", index.Conjunction{
			Action: []index.Predicate{{Op: index.Prefix, Value: "wallet.credential.r"}}}, 2},
		{"both predicates must hold", index.Conjunction{
			Action:  []index.Predicate{{Op: index.Equal, Value: "wallet.credential.issued"}},
			Outcome: []index.Predicate{{Op: index.Equal, Value: "failure"}}}, 2},
		{"a target the row carries", index.Conjunction{
			TargetType: []index.Predicate{{Op: index.Equal, Value: "credential"}}}, 6},
		{"an indexed extension property", index.Conjunction{
			Data: []index.PathPredicate{{
				Path: "/credential_type", Op: index.Equal, Text: "pid", Kind: index.Text}}}, 6},
		{"a time range, exclusive at the top", index.Conjunction{
			OccurredAt: []index.TimePredicate{{
				From: at(t, "2026-09-17T10:00:00Z"), To: at(t, "2026-09-17T10:02:00Z")}}}, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			page, err := m.Search(ctx, index.Query{
				Profile: "security", Filter: []index.Conjunction{c.with}, Limit: 100,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Rows) != c.want {
				t.Fatalf("%d rows, want %d: %v", len(page.Rows), c.want, searchIDs(t, page))
			}
		})
	}
}

// A row matching both conjunctions comes back once, not twice.
func TestMemoryConjunctionsAreOred(t *testing.T) {
	m := searchable(t)
	page, err := m.Search(context.Background(), index.Query{
		Profile: "security", Limit: 100,
		Filter: []index.Conjunction{
			{Action: []index.Predicate{{Op: index.Equal, Value: "wallet.credential.issued"}}},
			{Outcome: []index.Predicate{{Op: index.Equal, Value: "failure"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 4 {
		t.Fatalf("%d rows, want 4: %v", len(page.Rows), searchIDs(t, page))
	}
}

func TestMemoryFacetsAndGet(t *testing.T) {
	m := searchable(t)
	ctx := context.Background()
	facets, err := m.Facets(ctx, index.Query{Profile: "security"},
		[]string{index.FieldAction}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(facets) != 1 || facets[0].Values[0].Value != "wallet.credential.issued" ||
		facets[0].Values[0].Count != 4 {
		t.Fatalf("action facet: %+v", facets)
	}

	// The tail is hex and ends in a letter. A UUID whose last group is twelve
	// digits reads to the leak canary exactly like an AWS account id, which is
	// the point of the canary: it cannot tell them apart and should not try.
	id := "018f0000-0000-7000-8000-00000000000c"
	got, where, err := m.Get(ctx, "security", id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || where.ObjectKey == "" || where.Line == 0 {
		t.Fatalf("get: %+v %+v", got, where)
	}
	if _, _, err := m.Get(ctx, "security", "018f0000-0000-7000-8000-0000000000aa"); err == nil {
		t.Fatal("a record that was never written was found")
	}
}

// An unbounded OR is unbounded work, in either implementation.
func TestMemoryRefusesTooManyConjunctions(t *testing.T) {
	m := searchable(t)
	_, err := m.Search(context.Background(), index.Query{
		Profile: "security", Filter: make([]index.Conjunction, 5)})
	if err == nil {
		t.Fatal("five conjunctions were accepted")
	}
}
