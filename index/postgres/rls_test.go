package postgres_test

import (
	"context"
	"testing"

	"github.com/truvity/audit/index/indextest"
	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/pgtest"
)

// Row-level security is the second line under the grant, and these tests stand
// on it rather than on the SQL the searcher writes.
//
// Every other Postgres test connects as the role that owns the tables, and
// row-level security does not apply to an owner, so the policies would pass
// those tests by not applying. These connect as a reader role instead.
//
// The policies are for the case the application gets wrong. Query.Tenants is
// the grant and the searcher turns it into a term of the SQL; if that were the
// only thing between two customers, one missed term — in a new predicate, a
// new code path, a facet query — hands one customer the other's trail. So each
// check below reads the tables directly, with no tenant term at all, which is
// exactly what a forgotten term produces.
func TestTheTenantPolicyHoldsWhenTheQueryForgetsTo(t *testing.T) {
	owner := pgtest.Open(t)
	ctx := context.Background()
	idx, err := postgres.New(owner)
	if err != nil {
		t.Fatal(err)
	}
	corpus := indextest.Corpus(t)
	indextest.Index(t, idx, corpus)
	reader := pgtest.AsReader(t, owner)

	// visible counts what a pinned connection can see of both tables the
	// policies cover, by asking for everything.
	visible := func(t *testing.T, pin postgres.Pin) (events, counted map[string]int) {
		t.Helper()
		events, counted = map[string]int{}, map[string]int{}
		err := postgres.AsTenants(ctx, reader, pin, func(pinned *postgres.Index) error {
			for table, into := range map[string]map[string]int{
				"events_core": events, "facet_counts": counted,
			} {
				rows, err := pinned.DB.Query(ctx, "select tenant_id from "+table)
				if err != nil {
					return err
				}
				for rows.Next() {
					var tenant string
					if err := rows.Scan(&tenant); err != nil {
						rows.Close()
						return err
					}
					into[tenant]++
				}
				rows.Close()
				if err := rows.Err(); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return events, counted
	}

	for _, c := range []struct {
		name string
		pin  postgres.Pin
		want []string
	}{
		{"one tenant", postgres.Pin{Tenants: []string{"globex"}}, []string{"globex"}},
		{"the other tenant", postgres.Pin{Tenants: []string{"initech"}}, []string{"initech"}},
		// A grant names several tenants as often as one; a policy holding one
		// would have left every such reader with the first line alone.
		{"a grant of two", postgres.Pin{Tenants: []string{"globex", "initech"}}, []string{"globex", "initech"}},
		{"a tenant that does not exist", postgres.Pin{Tenants: []string{"hooli"}}, nil},
		// A tenant identifier is free text, so the list cannot be delimited by
		// anything a tenant might contain.
		{"a tenant with a comma in it", postgres.Pin{Tenants: []string{"globex,initech"}}, nil},
		{"every tenant, said explicitly", postgres.Pin{AllTenants: true}, []string{"globex", "initech"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			events, counted := visible(t, c.pin)
			for table, seen := range map[string]map[string]int{"events_core": events, "facet_counts": counted} {
				if len(seen) != len(c.want) {
					t.Fatalf("%s: want tenants %v, saw %v", table, c.want, seen)
				}
				for _, tenant := range c.want {
					if seen[tenant] == 0 {
						t.Fatalf("%s: %s's rows were hidden from a pin that names it: %v", table, tenant, seen)
					}
				}
			}
			// The whole share, so the policy is narrowing rather than
			// truncating.
			for _, tenant := range c.want {
				if want := share(corpus, tenant); events[tenant] != want {
					t.Fatalf("want %s's %d records, saw %d", tenant, want, events[tenant])
				}
			}
		})
	}

	// A reader that pinned nothing sees nothing. This is the property the first
	// policies had backwards: "unset" read as "everything", which made the
	// second line open exactly when somebody forgot it. It is also what catches
	// a pin that leaked session-wide from an earlier borrower of the pool: the
	// cases above ran on these same connections.
	t.Run("an unpinned reader sees nothing", func(t *testing.T) {
		var n int
		if err := reader.QueryRow(ctx, "select count(*) from events_core").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("an unpinned reader saw %d records; the policy must refuse by default", n)
		}
	})

	t.Run("a pin must say what it wants", func(t *testing.T) {
		for _, pin := range []postgres.Pin{{}, {Tenants: []string{"globex"}, AllTenants: true}} {
			if err := postgres.AsTenants(ctx, reader, pin, func(*postgres.Index) error { return nil }); err == nil {
				t.Fatalf("pin %+v was accepted", pin)
			}
		}
	})
}

// The reading service's index pins every read from the query's own tenant
// term, and over a reader role the whole conformance suite still passes — so
// the pin narrows what a forgotten term would expose, and changes no answer
// the suite knows the right answer to.
func TestAReaderIndexConforms(t *testing.T) {
	owner := pgtest.Open(t)
	idx, err := postgres.New(owner)
	if err != nil {
		t.Fatal(err)
	}
	indextest.Index(t, idx, indextest.Corpus(t))
	searcher, err := postgres.NewReader(pgtest.AsReader(t, owner))
	if err != nil {
		t.Fatal(err)
	}
	indextest.Run(t, "postgres-reader", searcher)

	// And Get, which has no tenant term, still finds a record.
	row, _, err := searcher.Get(context.Background(), indextest.Profile, indextest.ID(4))
	if err != nil {
		t.Fatal(err)
	}
	if row.ID != indextest.ID(4) {
		t.Fatalf("got %s", row.ID)
	}
}

// share is how many of the corpus's records belong to a tenant.
func share(corpus []indextest.Placed, tenant string) int {
	var n int
	for _, p := range corpus {
		if p.Tenant == tenant {
			n++
		}
	}
	return n
}
