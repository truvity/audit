package postgres

import (
	"context"
	"errors"
	"fmt"
)

// TenantSetting is the name the row-level security policies read.
const TenantSetting = "audit.tenant_id" // audit:not-an-action — a Postgres setting

// AsTenant runs a read with the connection pinned to one tenant, so that the
// row-level security policies apply to it.
//
// The policies have been in the schema since the first migration and nothing in
// this package ever set the value they read, which left every deployment to
// work out how for itself. The obvious way is wrong in a way that does not show
// up until it matters: `set_config(name, value, false)` is session-scoped, and a
// pooled connection carries it back into the pool, so the next borrower reads
// one tenant's trail under another tenant's name. A pool makes that a lottery
// rather than a bug anyone can reproduce.
//
// So the pin is made inside a transaction with the local flag set, which
// Postgres releases when the transaction ends however it ends. The transaction
// is read-only and always rolled back: this is a route for reading, and a
// caller that wanted to write would not want it pinned to one tenant anyway.
//
// It is a second line rather than the first. The grant in Query.Tenants is what
// the service enforces and what a reader is refused by; this is what holds when
// a new predicate, a new code path or a facet query forgets to AND it in.
func AsTenant(ctx context.Context, db DB, tenant string, fn func(*Index) error) error {
	if db == nil {
		return errors.New("postgres: a database is required")
	}
	if tenant == "" {
		// An empty value is what the policies read as "no pin at all", so
		// accepting one here would hand back an unrestricted index under a name
		// that promises the opposite.
		return errors.New("postgres: AsTenant needs a tenant; use New for a connection with no pin")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: pinning to tenant %s: %w", tenant, err)
	}
	// Rolled back in every path, including the happy one: nothing here writes.
	defer func() { _ = tx.Rollback(ctx) }()

	// set_config rather than SET, because SET takes no parameters and a tenant
	// spliced into SQL is the one place this package must not have.
	if _, err := tx.Exec(ctx, "select set_config($1, $2, true)", TenantSetting, tenant); err != nil {
		return fmt.Errorf("postgres: pinning to tenant %s: %w", tenant, err)
	}
	pinned, err := New(tx)
	if err != nil {
		return err
	}
	return fn(pinned)
}
