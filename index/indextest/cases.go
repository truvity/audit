package indextest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/index"
)

// Need is a capability a case depends on.
//
// It is what makes this suite mean something for a searcher that cannot do
// everything. A case whose need a searcher does not declare is not skipped: the
// searcher is required to refuse it. Skipping would let an implementation pass
// the whole suite by declaring nothing and answering wrongly, which is the
// failure mode a conformance suite exists to prevent.
type Need int

// The capabilities a case can need.
const (
	// Nothing is a case every searcher must answer.
	Nothing Need = iota
	// DataPredicates filters on an extension property.
	DataPredicates
	// SortRecordedAt orders by when the record was recorded rather than when it
	// happened.
	SortRecordedAt
	// CountFacets counts values.
	CountFacets
)

// Case is one question and the answer every searcher that can answer it must
// give.
type Case struct {
	Name  string
	Need  Need
	Query index.Query
	// Want is the corpus positions expected, in order. Written out rather than
	// derived: an expectation computed from one implementation makes that
	// implementation the specification.
	Want []int
	// Facets is the expected counts, for a case that needs them.
	Facets []index.Facet
}

// Cases are the questions. Adding one here asks it of every searcher at once,
// which is the only reason this package is worth its weight.
func Cases() []Case {
	all := index.Query{
		Profile: Profile,
		Sort:    []index.SortBy{{Field: index.SortOccurredAt, Descending: true}},
		Limit:   20,
	}
	with := func(f ...index.Conjunction) index.Query {
		q := all
		q.Filter = f
		return q
	}
	return []Case{{
		Name:  "every record of the profile, newest first",
		Query: all,
		Want:  []int{8, 7, 6, 5, 4, 3, 2, 1, 0},
	}, {
		Name: "a grant narrows to one tenant",
		// This is the case the whole authorisation model rests on: the grant is
		// not a filter the caller wrote and could leave out, it is a term the
		// service AND-s in. A searcher that ignored Tenants would hand one
		// customer another's trail.
		Query: func() index.Query { q := all; q.Tenants = []string{"globex"}; return q }(),
		Want:  []int{5, 4, 3},
	}, {
		Name:  "one action",
		Query: with(index.Conjunction{Action: []index.Predicate{{Op: index.Equal, Value: "wallet.credential.revoked"}}}),
		Want:  []int{7, 4, 1},
	}, {
		Name: "several actors by In",
		Query: with(index.Conjunction{ActorID: []index.Predicate{
			{Op: index.In, Values: []string{"olga", "ivan"}}}}),
		Want: []int{8, 7, 6, 5, 2, 1, 0},
	}, {
		Name: "everything that did not succeed",
		Query: with(index.Conjunction{Outcome: []index.Predicate{
			{Op: index.NotEqual, Value: "success"}}}),
		Want: []int{5, 2},
	}, {
		Name: "actors whose identifier starts with a prefix",
		Query: with(index.Conjunction{ActorID: []index.Predicate{
			{Op: index.Prefix, Value: "svc-"}}}),
		Want: []int{4, 3},
	}, {
		Name: "a time range, with the end excluded",
		Query: with(index.Conjunction{OccurredAt: []index.TimePredicate{
			{From: Late, To: Late.Add(3 * time.Minute)}}}),
		Want: []int{6, 5, 4},
	}, {
		Name: "two conjunctions are OR-ed",
		Query: with(
			index.Conjunction{Action: []index.Predicate{{Op: index.Equal, Value: "wallet.credential.revoked"}}},
			index.Conjunction{ActorKind: []index.Predicate{{Op: index.Equal, Value: "service"}}},
		),
		Want: []int{7, 4, 3, 1},
	}, {
		Name: "two predicates in one conjunction are AND-ed",
		Query: with(index.Conjunction{
			Action:    []index.Predicate{{Op: index.Equal, Value: "wallet.credential.issued"}},
			ActorKind: []index.Predicate{{Op: index.Equal, Value: "operator"}},
		}),
		Want: []int{8, 6, 5, 2, 0},
	}, {
		Name: "a target the record names",
		Query: with(index.Conjunction{TargetID: []index.Predicate{
			{Op: index.Equal, Value: "cred-4"}}}),
		Want: []int{4},
	}, {
		Name: "a text extension property",
		Need: DataPredicates,
		Query: with(index.Conjunction{Data: []index.PathPredicate{
			{Path: "/credential_type", Op: index.Equal, Kind: index.Text, Text: "mdl"}}}),
		Want: []int{8, 6, 3, 2},
	}, {
		Name: "a numeric extension property",
		Need: DataPredicates,
		Query: with(index.Conjunction{Data: []index.PathPredicate{
			{Path: "/batch", Op: index.Equal, Kind: index.Int, Int: 3}}}),
		Want: []int{5, 4},
	}, {
		Name: "a time extension property",
		Need: DataPredicates,
		// Stored in its own column, as a timestamp, so it is compared as an
		// instant rather than as the string it arrived as.
		Query: with(index.Conjunction{Data: []index.PathPredicate{{
			Path: "/expires_at", Op: index.Equal, Kind: index.Time,
			At: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		}}}),
		Want: []int{3, 2, 1, 0},
	}, {
		Name: "a boolean extension property",
		Need: DataPredicates,
		// Held as an integer, which is how two kinds come to share a column —
		// and why a searcher that stored a boolean as text would answer a
		// neighbouring question here.
		Query: with(index.Conjunction{Data: []index.PathPredicate{
			{Path: "/renewable", Op: index.Equal, Kind: index.Bool, Int: 1}}}),
		Want: []int{8, 6, 4, 2, 0},
	}, {
		Name: "ordered by when it was recorded, not when it happened",
		Need: SortRecordedAt,
		Query: func() index.Query {
			q := all
			q.Sort = []index.SortBy{{Field: index.SortRecordedAt, Descending: true}}
			return q
		}(),
		// Deliberately not the occurred_at order: the corpus records arrive out
		// of the order they happened in, as a queue that retried for a while would deliver
		// them, so a searcher that sorts by the wrong column is caught here
		// rather than flattered by a fixture where the two agree.
		Want: []int{6, 7, 4, 8, 5, 2, 3, 0, 1},
	}, {
		Name:  "counting the values of a field",
		Need:  CountFacets,
		Query: all,
		Facets: []index.Facet{{
			Field: index.FieldOutcome,
			Values: []index.FacetValue{
				{Value: "success", Count: 7},
				{Value: "failure", Count: 2},
			},
		}},
	}}
}

// offers says whether a searcher claims it can answer a case.
func offers(c Capabilities, need Need) bool {
	switch need {
	case DataPredicates:
		return c.DataPredicates
	case SortRecordedAt:
		for _, f := range c.SortFields {
			if f == index.SortRecordedAt {
				return true
			}
		}
		return false
	case CountFacets:
		return c.Facets
	default:
		return true
	}
}

// Capabilities is index.Capabilities, named here so the file reads without the
// package qualifier on every line.
type Capabilities = index.Capabilities

// Run asks every case of one searcher.
//
// A searcher must answer exactly, in order, the cases its capabilities cover,
// and must return an error for the ones they do not. The second half is the
// point: it is what stops an implementation passing by declaring nothing, and
// it catches the opposite fault too — a searcher that quietly answers a query
// it told the caller it could not, which is how a caller ends up trusting a
// narrower answer than it asked for.
func Run(t *testing.T, name string, searcher index.Searcher) {
	t.Helper()
	caps := searcher.Capabilities()
	ctx := context.Background()
	var answered int

	for _, c := range Cases() {
		t.Run(name+"/"+c.Name, func(t *testing.T) {
			can := offers(caps, c.Need)
			if c.Facets != nil {
				runFacets(ctx, t, searcher, c, can)
				if can {
					answered++
				}
				return
			}
			page, err := searcher.Search(ctx, c.Query)
			if !can {
				if err == nil {
					t.Fatalf("this searcher does not offer %v, yet it answered with %d rows "+
						"instead of refusing; a caller would trust an answer narrower than it asked for",
						c.Need, len(page.Rows))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			answered++
			same(t, page.Rows, c.Want)
		})
	}

	// An absence is not an outage. A searcher that reports a missing record
	// as a plain error has the service tell the client to retry, forever.
	t.Run(name+"/get of an unknown id is ErrNotFound", func(t *testing.T) {
		// A well-formed id that no record has: absence, not malformation.
		_, _, err := searcher.Get(ctx, Profile, "00000000-0000-7000-8000-00000000beef")
		if !errors.Is(err, index.ErrNotFound) {
			t.Fatalf("got %v, want index.ErrNotFound", err)
		}
	})

	t.Run(name+"/paging reaches every record exactly once", func(t *testing.T) {
		paging(ctx, t, searcher)
	})

	// A searcher that refused everything would have reached here with a clean
	// run and proved nothing at all.
	t.Run(name+"/answered something", func(t *testing.T) {
		if answered == 0 {
			t.Fatal("this searcher refused every case; the suite proved nothing about it")
		}
	})
}

// paging walks the whole corpus three at a time.
//
// Every case above asks for one page, which is the shape that hides the two
// ways a cursor goes wrong: a boundary that excludes too much drops a record
// silently, and one that excludes too little hands it out twice. Neither shows
// in a single page, and both are what a reader would report as "the trail is
// missing an event" long after anyone could tell why.
//
// It is asked of every searcher because a cursor is the one piece of a searcher
// that a caller carries between requests and cannot inspect.
func paging(ctx context.Context, t *testing.T, searcher index.Searcher) {
	t.Helper()
	const page = 3
	q := index.Query{
		Profile: Profile,
		Sort:    []index.SortBy{{Field: index.SortOccurredAt, Descending: true}},
		Limit:   page,
	}
	want := []int{8, 7, 6, 5, 4, 3, 2, 1, 0}

	var got []index.Row
	// One more round than the corpus needs, so that a searcher which never says
	// it has finished fails here rather than looping.
	for round := 0; round <= len(want)/page+1; round++ {
		out, err := searcher.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Rows) > page {
			t.Fatalf("round %d: asked for %d rows and got %d", round, page, len(out.Rows))
		}
		got = append(got, out.Rows...)
		if !out.More {
			same(t, got, want)
			return
		}
		if out.Next == nil {
			t.Fatalf("round %d: there is more, and no cursor to reach it with", round)
		}
		q.After = out.Next
	}
	t.Fatalf("paging did not finish: %d rows and still more", len(got))
}

func runFacets(ctx context.Context, t *testing.T, searcher index.Searcher, c Case, can bool) {
	t.Helper()
	fields := make([]string, 0, len(c.Facets))
	for _, f := range c.Facets {
		fields = append(fields, f.Field)
	}
	got, err := searcher.Facets(ctx, c.Query, fields, 10)
	if !can {
		if err == nil {
			t.Fatalf("this searcher does not offer facets, yet it counted: %v", got)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c.Facets) {
		t.Fatalf("facets\n got %+v\nwant %+v", got, c.Facets)
	}
}

// same compares the rows a searcher returned with the corpus positions expected.
func same(t *testing.T, rows []index.Row, want []int) {
	t.Helper()
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, r.ID)
	}
	expect := make([]string, 0, len(want))
	for _, n := range want {
		expect = append(expect, ID(n))
	}
	if !reflect.DeepEqual(got, expect) {
		t.Fatalf("rows differ\n got %d: %s\nwant %d: %s",
			len(got), positions(got), len(expect), positions(expect))
	}
}

// positions names rows by their place in the corpus, because a failure that
// prints nine UUIDs differing in one character tells a reader nothing.
func positions(ids []string) string {
	var out []string
	for _, got := range ids {
		found := "?" + got
		for n := 0; n < 32; n++ {
			if ID(n) == got {
				found = fmt.Sprintf("#%d", n)
				break
			}
		}
		out = append(out, found)
	}
	return "[" + strings.Join(out, " ") + "]"
}
