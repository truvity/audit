// Package pgtest gives a test its own Postgres schema.
//
// The index is the one part of this repository that cannot be tested without a
// database. Rather than mock it — a mocked index would agree with whatever the
// code did and prove nothing about idempotency, which is the whole contract —
// the tests that need Postgres skip when none is configured. A contributor
// without one still runs everything else, and `just check` stays hermetic.
package pgtest

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/truvity/audit/index/postgres"
)

// URLEnv names the database the Postgres tests run against.
const URLEnv = "AUDIT_POSTGRES_URL"

// Open returns a migrated pool with a schema of its own, dropped when the test
// ends. A schema per test is what lets them run in parallel without undoing
// each other's rows, and it costs one statement.
func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(URLEnv)
	if dsn == "" {
		t.Skip("set " + URLEnv + " to run the Postgres tests")
	}

	ctx := context.Background()
	schema := schemaName(t.Name())
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, statement := range []string{
		"drop schema if exists " + schema + " cascade",
		"create schema " + schema,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanup, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Exec(context.Background(), "drop schema if exists "+schema+" cascade")
	})
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

// Reader is the role a reading service connects as.
//
// It exists because row-level security does not apply to the role that owns the
// tables, and the tests all connect as that role. A policy exercised only by
// its owner is a policy nobody has read: it passes whatever it says. So a test
// about isolation has to come in as somebody else, and this is the somebody.
const Reader = "audit_reader"

// AsReader returns a pool on the same schema connected as a role that row-level
// security applies to, with select granted and nothing else.
//
// It is deliberately not given insert: a reader that could write the trail it
// reads is not a reader, and the grant is the place that has to say so.
func AsReader(t *testing.T, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	dsn := os.Getenv(URLEnv)
	schema := schemaName(t.Name())

	// The role is cluster-wide and shared by every test that asks for it, so
	// creating it races. Losing the race is fine: what matters is that it is
	// there afterwards.
	if _, err := pool.Exec(ctx, `do $$ begin
		create role `+Reader+` login password 'reader' nosuperuser nocreatedb nocreaterole;
	exception when duplicate_object then null; end $$`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"grant usage on schema " + schema + " to " + Reader,
		"grant select on all tables in schema " + schema + " to " + Reader,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.User = Reader
	config.ConnConfig.Password = "reader"
	config.ConnConfig.RuntimeParams["search_path"] = schema
	reader, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	return reader
}

// schemaName turns a test's name into an identifier Postgres will take.
func schemaName(name string) string {
	return "t_" + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, strings.ToLower(name))
}
