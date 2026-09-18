package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/pgtest"
)

// corpus writes rows a search can be asked about.
func corpus(t *testing.T) (*postgres.Index, []index.Row) {
	t.Helper()
	pool := pgtest.Open(t)
	i, err := postgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	base := at(t, "2026-09-17T10:00:00Z")
	var rows []index.Row
	for n := 0; n < 6; n++ {
		r := row(t, id(n), base.Add(time.Duration(n)*time.Minute))
		r.OccurredAt = base.Add(time.Duration(n) * time.Minute)
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
		r.ActorID = "ps_" + string("abcdef"[n%6])
		r.Data = []index.Value{{Path: "/credential_type", Kind: index.Text,
			Text: map[bool]string{true: "pid", false: "mdl"}[n%2 == 0]}}
		rows = append(rows, r)
	}
	if err := i.Index(context.Background(), "security", rows); err != nil {
		t.Fatal(err)
	}
	return i, rows
}

// id is a fixture identifier. The tail is hex because the column is a uuid,
// which is the kind of thing a test finds for you the first time it reaches a
// real database rather than a map.
func id(n int) string {
	return "018f0000-0000-7000-8000-00000000000" + string("0123456789abcdef"[n%16])
}

func ids(rows []index.Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// The grant is one more term in the query, so there is no path to a row outside
// it: a query that forgot to apply it would have to forget to name a profile.
func TestSearchSeesOnlyTheGrantedTenants(t *testing.T) {
	i, _ := corpus(t)
	page, err := i.Search(context.Background(), index.Query{
		Profile: "security", Tenants: []string{"acme"},
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

// Paging is keyset, so a page is bounded by the last row rather than by how
// many rows precede it — and a record written meanwhile cannot shift a page
// that has already been handed out.
func TestSearchPagesWithoutRepeatingOrSkipping(t *testing.T) {
	i, _ := corpus(t)
	ctx := context.Background()
	q := index.Query{Profile: "security", Limit: 2,
		Sort: []index.SortBy{{Field: index.SortOccurredAt}}}

	var seen []string
	for page := 0; page < 5; page++ {
		got, err := i.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, ids(got.Rows)...)
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
	// In order, oldest first, as asked.
	for n := range seen {
		if seen[n] != id(n) {
			t.Fatalf("out of order at %d: %v", n, seen)
		}
	}
}

// The last page still carries a boundary, because a tail keeps polling it and a
// record recorded afterwards has to come back through it.
func TestTheLastPageStillCarriesItsPlace(t *testing.T) {
	i, _ := corpus(t)
	ctx := context.Background()
	q := index.Query{Profile: "security", Limit: 100,
		Sort: []index.SortBy{{Field: index.SortRecordedAt}}}

	page, err := i.Search(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if page.More {
		t.Fatal("six rows did not fit in a hundred")
	}
	if page.Next == nil {
		t.Fatal("the last page has no boundary, so a tail cannot resume from it")
	}

	// Nothing new: the same boundary returns nothing and keeps its place.
	q.After = page.Next
	empty, err := i.Search(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Rows) != 0 {
		t.Fatalf("%d rows after the end", len(empty.Rows))
	}
	if empty.Next == nil {
		t.Fatal("an empty page lost the boundary it was asked from")
	}

	// A record recorded later comes back through it.
	late := row(t, id(9), at(t, "2026-09-17T11:00:00Z"))
	if err := i.Index(ctx, "security", []index.Row{late}); err != nil {
		t.Fatal(err)
	}
	q.After = empty.Next
	caught, err := i.Search(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(caught.Rows) != 1 || caught.Rows[0].ID != id(9) {
		t.Fatalf("the tail missed a record recorded after the last page: %v", ids(caught.Rows))
	}
}

func TestSearchFiltersOnCoreFields(t *testing.T) {
	i, _ := corpus(t)
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
		{"in", index.Conjunction{
			ActorID: []index.Predicate{{Op: index.In, Values: []string{"ps_a", "ps_c"}}}}, 2},
		{"prefix", index.Conjunction{
			Action: []index.Predicate{{Op: index.Prefix, Value: "wallet.credential.r"}}}, 2},
		{"two predicates must both hold", index.Conjunction{
			Action:  []index.Predicate{{Op: index.Equal, Value: "wallet.credential.issued"}},
			Outcome: []index.Predicate{{Op: index.Equal, Value: "failure"}}}, 2},
		{"a target the row carries", index.Conjunction{
			TargetType: []index.Predicate{{Op: index.Equal, Value: "credential"}}}, 6},
		{"an indexed extension property", index.Conjunction{
			Data: []index.PathPredicate{{
				Path: "/credential_type", Op: index.Equal, Text: "pid", Kind: index.Text}}}, 3},
		{"a time range, exclusive at the top", index.Conjunction{
			OccurredAt: []index.TimePredicate{{
				From: at(t, "2026-09-17T10:00:00Z"), To: at(t, "2026-09-17T10:02:00Z")}}}, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			page, err := i.Search(ctx, index.Query{
				Profile: "security", Filter: []index.Conjunction{c.with}, Limit: 100,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Rows) != c.want {
				t.Fatalf("%d rows, want %d: %v", len(page.Rows), c.want, ids(page.Rows))
			}
		})
	}
}

// Conjunctions are OR-joined, so a row matching either comes back once.
func TestConjunctionsAreOredAndRowsAreNotDoubled(t *testing.T) {
	i, _ := corpus(t)
	page, err := i.Search(context.Background(), index.Query{
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
		t.Fatalf("%d rows, want 4: %v", len(page.Rows), ids(page.Rows))
	}
}

// An unbounded OR is unbounded work on a table that only grows.
func TestTooManyConjunctionsAreRefused(t *testing.T) {
	i, _ := corpus(t)
	filter := make([]index.Conjunction, 5)
	_, err := i.Search(context.Background(), index.Query{Profile: "security", Filter: filter})
	if err == nil {
		t.Fatal("five conjunctions were accepted")
	}
}

// The counts the writer kept answer the unfiltered case without touching the
// events; a filtered facet has to count them.
func TestFacetsBothWays(t *testing.T) {
	i, _ := corpus(t)
	ctx := context.Background()

	counted, err := i.Facets(ctx, index.Query{Profile: "security"},
		[]string{index.FieldAction, index.FieldOutcome}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(counted) != 2 {
		t.Fatalf("%d facets", len(counted))
	}
	if counted[0].Values[0].Value != "wallet.credential.issued" || counted[0].Values[0].Count != 4 {
		t.Fatalf("action facet: %+v", counted[0].Values)
	}

	filtered, err := i.Facets(ctx, index.Query{
		Profile: "security",
		Filter: []index.Conjunction{{
			Outcome: []index.Predicate{{Op: index.Equal, Value: "failure"}}}},
	}, []string{index.FieldAction}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered[0].Values) != 1 || filtered[0].Values[0].Count != 2 {
		t.Fatalf("filtered action facet: %+v", filtered[0].Values)
	}
}

// An answer a reader can check against the copy the digest chain accounts for
// is worth more than one they have to believe.
func TestGetCarriesProvenance(t *testing.T) {
	i, _ := corpus(t)
	got, where, err := i.Get(context.Background(), "security", id(2))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id(2) {
		t.Fatalf("got %s", got.ID)
	}
	if where.ObjectKey == "" || where.Line == 0 {
		t.Fatalf("no provenance: %+v", where)
	}
}

func TestGetIsAnErrorForSomethingNotThere(t *testing.T) {
	i, _ := corpus(t)
	if _, _, err := i.Get(context.Background(), "security", id(7)); err == nil {
		t.Fatal("a record that was never written was found")
	}
}
