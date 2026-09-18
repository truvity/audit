package postgres_test

import (
	"context"
	"testing"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/index/indextest"
	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/pgtest"
)

// Row-level security is the backstop under the grant, and this is the first
// test that actually stands on it.
//
// Every other Postgres test connects as the role that owns the tables, and
// row-level security does not apply to an owner. So the policies have been in
// the schema since the first migration and have never once been enforced during
// a test run: they passed by not applying. That is the same failure as a memory
// store that refuses deletion because it was written to refuse deletion.
//
// What the policy is for is the case the application gets wrong. `Query.Tenants`
// is the grant and the searcher AND-s it into the SQL, and if that were the only
// thing standing between two customers then one missed AND — in a new predicate,
// a new code path, a facet query nobody thought about — hands one customer the
// other's trail. The policy is there so that a connection carrying the wrong
// tenant sees nothing whatever the SQL asks for.
func TestTheTenantPolicyHoldsWhenTheQueryForgetsTo(t *testing.T) {
	owner := pgtest.Open(t)
	ctx := context.Background()
	idx, err := postgres.New(owner)
	if err != nil {
		t.Fatal(err)
	}
	indextest.Index(t, idx, indextest.Corpus(t))

	reader := pgtest.AsReader(t, owner)
	searcher, err := postgres.New(reader)
	if err != nil {
		t.Fatal(err)
	}

	// A query with no tenant term at all: as far as the SQL is concerned this
	// asks for the whole profile, which is exactly what a forgotten grant would
	// produce.
	everything := index.Query{
		Profile: indextest.Profile,
		Sort:    []index.SortBy{{Field: index.SortOccurredAt, Descending: true}},
		Limit:   50,
	}

	for _, tenant := range []string{"globex", "initech"} {
		t.Run("a connection pinned to "+tenant, func(t *testing.T) {
			err := postgres.AsTenant(ctx, reader, tenant, func(pinned *postgres.Index) error {
				page, err := pinned.Search(ctx, everything)
				if err != nil {
					return err
				}
				if len(page.Rows) == 0 {
					t.Fatal("the policy hid everything, including this tenant's own records")
				}
				for _, r := range page.Rows {
					if r.TenantID != tenant {
						t.Fatalf("a connection pinned to %s was shown %s's record %s",
							tenant, r.TenantID, r.ID)
					}
				}
				// And the count is the tenant's whole share, so the policy is
				// narrowing rather than truncating.
				if want := share(t, tenant); len(page.Rows) != want {
					t.Fatalf("want %s's %d records, got %d", tenant, want, len(page.Rows))
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}

	// Ordinary borrowing from the same pool afterwards is unrestricted. This is
	// the half that would catch a pin made session-wide instead of locally:
	// that pin would come back out of the pool attached to whoever borrowed
	// next, and this case would see one tenant's rows where the whole corpus
	// belongs.
	t.Run("the pin does not survive back into the pool", func(t *testing.T) {
		page, err := searcher.Search(ctx, everything)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Rows) != len(indextest.Corpus(t)) {
			t.Fatalf("want the whole corpus for an unpinned connection, got %d", len(page.Rows))
		}
	})
}

// share is how many of the corpus's records belong to a tenant.
func share(t *testing.T, tenant string) int {
	t.Helper()
	var n int
	for _, p := range indextest.Corpus(t) {
		if p.Tenant == tenant {
			n++
		}
	}
	return n
}
