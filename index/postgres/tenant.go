package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// The settings the row-level security policies read. Both are local to a
// transaction when AsTenants sets them.
const (
	// TenantsSetting holds a JSON array of the tenants a connection may see.
	TenantsSetting = "audit.tenant_ids" // audit:not-an-action — a Postgres setting
	// AllTenantsSetting, when "on", lets a connection see every tenant.
	AllTenantsSetting = "audit.all_tenants" // audit:not-an-action — a Postgres setting
)

// Pin is what a connection is allowed to see. It is a grant's tenant half, in
// the shape the policies read.
type Pin struct {
	Tenants    []string
	AllTenants bool
}

// PinOf is the pin a query's tenant term implies. An empty list means every
// tenant, as it does in index.Query, where only an operator's grant leaves it
// empty.
func PinOf(tenants []string) Pin {
	if len(tenants) == 0 {
		return Pin{AllTenants: true}
	}
	return Pin{Tenants: tenants}
}

// AsTenants runs a read with the connection pinned, so that the row-level
// security policies bind it.
//
// The pin is made inside a transaction with the local flag, which Postgres
// releases when the transaction ends however it ends. The obvious alternative,
// set_config(name, value, false), is session-scoped, and a pooled connection
// carries it back into the pool: the next borrower would read under the
// previous caller's tenants. The transaction is always rolled back, because
// nothing here writes.
//
// The policies refuse by default: a reader connection that sets nothing sees
// nothing. This is the second line, under the service's own narrowing; it holds
// when a query forgets its tenant term, not when the grant itself is wrong.
func AsTenants(ctx context.Context, db DB, pin Pin, fn func(*Index) error) error {
	if db == nil {
		return errors.New("postgres: a database is required")
	}
	if pin.AllTenants == (len(pin.Tenants) > 0) {
		// Both is ambiguous and neither would see nothing, which is a caller
		// that forgot to say what it wanted rather than a query worth running.
		return errors.New("postgres: a pin names tenants or all tenants, exactly one of the two")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: pinning a read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// set_config rather than SET: SET takes no parameters, and a tenant spliced
	// into SQL is the one thing this package must never have.
	if pin.AllTenants {
		_, err = tx.Exec(ctx, "select set_config($1, 'on', true)", AllTenantsSetting)
	} else {
		var list []byte
		if list, err = json.Marshal(pin.Tenants); err == nil {
			_, err = tx.Exec(ctx, "select set_config($1, $2, true)", TenantsSetting, string(list))
		}
	}
	if err != nil {
		return fmt.Errorf("postgres: pinning a read: %w", err)
	}
	pinned, err := New(tx)
	if err != nil {
		return err
	}
	return fn(pinned)
}

// NewReader returns an index whose every read is pinned to the tenants its
// query names.
//
// This is what a reading service uses. Over a connection whose role owns the
// tables the pin changes nothing, since an owner bypasses the policies; over a
// reader role it is what makes them apply. So it is safe to use either way,
// and a deployment gets the second line by choosing the role, not by
// rebuilding the service.
//
// Get takes no tenants — index.Searcher gives it none — so it is pinned to
// every tenant and the caller checks the row it gets back, as the query
// service does. A lookup by identifier has no tenant term to forget.
func NewReader(db DB) (*Index, error) {
	i, err := New(db)
	if err != nil {
		return nil, err
	}
	i.reader = true
	return i, nil
}
