package suite

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestQueryRoleCanReadAndCannotWrite proves the separation
// index/postgres/postgres.go's GrantReader exists for: the migrate job's
// `--reader` grant gives the query role SELECT and nothing else. A role
// that could also write would make row-level security (were this
// deployment to compose a profile that needs it) rest on the query
// service alone rather than on Postgres.
func TestQueryRoleCanReadAndCannotWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db := openDSN(ctx, t, shared.queryDSN)
	defer func() { _ = db.Close() }()

	// The migration completed and the grant reached this role: a bare
	// SELECT succeeds. It legitimately returns zero rows — events_core's
	// row-level security policy denies every tenant to a session that has
	// not set audit.tenant_ids (index/postgres/schema/0004_tenant_list.sql);
	// TestRecordIsWrittenAndIndexed is what proves a row set that way is
	// actually there.
	if _, err := db.QueryContext(ctx, `select count(*) from events_core`); err != nil {
		t.Fatalf("select on events_core as the query role: %v", err)
	}

	// The query role cannot write. SQLSTATE 42501 (insufficient_privilege)
	// is what the grant refuses; anything else would mean the role never
	// really got only SELECT.
	_, err := db.ExecContext(ctx,
		`insert into events_core (profile, id, recorded_at, tenant_id, occurred_at, source, action, operation, outcome, object_key, line)
		 values ('security', gen_random_uuid(), now(), 't', now(), 'audit', 'audit.search', 'access', 'success', 'x', 0)`)
	if err == nil {
		t.Fatal("the query role was able to INSERT into events_core — it is not separated from the writer role")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("INSERT as the query role failed with %v, wanted a permission-denied error", err)
	}
}

// TestWriterRoleHasNoIndex proves the other half of the separation: the
// migrate job's `--writer` grant gives the write path the deduplication table
// and the registry and none of the index, which the indexer writes. A writer
// that could read or write the index would make "the writer does not index" a
// convention and not a fact of the database.
func TestWriterRoleHasNoIndex(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := openDSN(ctx, t, shared.writerDSN)
	defer func() { _ = db.Close() }()

	// What it does hold: the deduplication table.
	if _, err := db.ExecContext(ctx, `select count(*) from seen`); err != nil {
		t.Fatalf("select on seen as the writer role: %v", err)
	}
	for _, statement := range []string{
		`select count(*) from events_core`,
		`select count(*) from index_cursor`,
		`delete from events_core`,
	} {
		_, err := db.ExecContext(ctx, statement)
		if err == nil {
			t.Fatalf("the writer role ran %q: it is not separated from the indexer's", statement)
		}
		if !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("%q as the writer role failed with %v, wanted a permission-denied error", statement, err)
		}
	}
}
