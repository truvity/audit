package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/index/indextest"
	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/pgtest"
)

// The parts of an installation connect as three roles, and these tests stand on
// what each is refused as much as on what it is given. Everything else here
// connects as the owner, which is bound by none of it.

type roles struct {
	owner, writer, observe, reader *pgxpool.Pool
}

func withRoles(t *testing.T) roles {
	t.Helper()
	owner := pgtest.Open(t)
	grant := func(f func(context.Context, postgres.DB, string) error, role string) func(context.Context) error {
		return func(ctx context.Context) error { return f(ctx, owner, role) }
	}
	return roles{
		owner:   owner,
		writer:  pgtest.AsRole(t, owner, pgtest.Writer, grant(postgres.GrantWriter, pgtest.Writer)),
		observe: pgtest.AsRole(t, owner, pgtest.Observe, grant(postgres.GrantObserver, pgtest.Observe)),
		reader:  pgtest.AsReader(t, owner),
	}
}

func refused(err error) bool {
	var e *pgconn.PgError
	return errors.As(err, &e) && (e.Code == "42501" || e.Code == "42P01")
}

// The writer holds the deduplication table and the registry and no part of the
// index: a writer that is compromised cannot rewrite what search answers with,
// and a writer that does not index has no reason to be able to.
func TestTheWriterRoleHasNoIndex(t *testing.T) {
	r := withRoles(t)
	ctx := context.Background()

	dedupe, err := postgres.NewDedupe(r.writer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := dedupe.Mark(ctx, []string{"a", "b"}); err != nil {
		t.Fatalf("the writer cannot mark what it wrote: %v", err)
	}
	if seen, err := dedupe.Seen(ctx, []string{"a", "c"}); err != nil || !seen["a"] || seen["c"] {
		t.Fatalf("the writer cannot ask what it has written: %v %v", seen, err)
	}
	if err := postgres.CheckVersion(ctx, r.writer); err != nil {
		t.Fatalf("the writer cannot check the schema version: %v", err)
	}
	if err := postgres.BindKeyDirectory(ctx, r.writer, "directory-1"); err != nil {
		t.Fatalf("the writer cannot bind its key directory: %v", err)
	}
	if _, err := r.writer.Exec(ctx, `insert into catalogues (source, version, document) values ('s', '1', 'x')`); err != nil {
		t.Fatalf("the writer cannot keep a registered catalogue: %v", err)
	}

	for _, statement := range []string{
		`select 1 from events_core`,
		`select 1 from events_context`,
		`select 1 from events_data`,
		`select 1 from facet_counts`,
		`select 1 from index_cursor`,
		`delete from events_core`,
		`select audit_ensure_month('2026-10-01')`,
	} {
		if _, err := r.writer.Exec(ctx, statement); !refused(err) {
			t.Errorf("the writer role ran %q: %v", statement, err)
		}
	}
}

// The indexer writes the index and its cursors, creates partitions through the
// one function it is given, and cannot mark a record as written or register a
// catalogue.
func TestTheObserveRoleHasTheIndexAndNothingElse(t *testing.T) {
	r := withRoles(t)
	ctx := context.Background()

	// A fresh database has no partition for the corpus's months: observe is not
	// the owner, so this is the function doing what the owner would have done.
	target, err := postgres.New(r.observe)
	if err != nil {
		t.Fatal(err)
	}
	corpus := indextest.Corpus(t)
	indextest.Index(t, target, corpus)
	if err := target.Advance(ctx, "security", "acme", "records/security/acme/2026/10/03/10/X", nil); err != nil {
		t.Fatalf("observe cannot move a cursor: %v", err)
	}
	if got, err := target.Cursor(ctx, "security", "acme"); err != nil || got == "" {
		t.Fatalf("observe cannot read its cursor back: %q %v", got, err)
	}
	if err := target.ResetCursor(ctx, "security", ""); err != nil {
		t.Fatalf("observe cannot reset a cursor: %v", err)
	}
	var count int
	if err := r.owner.QueryRow(ctx, `select count(*) from events_core`).Scan(&count); err != nil || count == 0 {
		t.Fatalf("observe's rows are not in the index: %d %v", count, err)
	}

	for _, statement := range []string{
		`select 1 from seen`,
		`insert into seen (id) values ('x')`,
		`select 1 from catalogues`,
		`select 1 from audit_key_directory`,
		`create table observe_made_this (x int)`,
		`delete from events_core`,
		`update events_context set actor_id = ''`,
	} {
		if _, err := r.observe.Exec(ctx, statement); !refused(err) {
			t.Errorf("the observe role ran %q: %v", statement, err)
		}
	}
}

// The reader reads the index and can write nothing in it, and sees nothing the
// index's tenant policy does not give it. It is given no cursors and no
// deduplication table.
func TestTheReaderRoleOnlyReadsTheIndex(t *testing.T) {
	r := withRoles(t)
	ctx := context.Background()
	idx, err := postgres.New(r.owner)
	if err != nil {
		t.Fatal(err)
	}
	indextest.Index(t, idx, indextest.Corpus(t))

	if _, err := postgres.NewReader(r.reader); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`insert into index_cursor (profile, tenant_id, last_key) values ('p', 't', 'k')`,
		`select 1 from index_cursor`,
		`select 1 from seen`,
		`select 1 from catalogues`,
		`delete from events_core`,
		`update events_core set action = ''`,
	} {
		if _, err := r.reader.Exec(ctx, statement); !refused(err) {
			t.Errorf("the reader role ran %q: %v", statement, err)
		}
	}
	// Select is granted, but a reader that named no tenant sees nothing.
	var n int
	if err := r.reader.QueryRow(ctx, `select count(*) from events_core`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a reader that named no tenant saw %d rows: %v", n, err)
	}
}

// Naming one role for two parts would be separation in name only, and so is
// refused; and so is naming the owner.
func TestGrantRolesRefusesWhatWouldNotSeparate(t *testing.T) {
	owner := pgtest.Open(t)
	ctx := context.Background()
	if err := postgres.GrantRoles(ctx, owner, postgres.Roles{Writer: "audit_x", Observe: "audit_x"}); err == nil ||
		!strings.Contains(err.Error(), "different") {
		t.Errorf("one role for two parts was accepted: %v", err)
	}
	var me string
	if err := owner.QueryRow(ctx, `select current_user`).Scan(&me); err != nil {
		t.Fatal(err)
	}
	if err := postgres.GrantRoles(ctx, owner, postgres.Roles{Observe: me}); err == nil ||
		!strings.Contains(err.Error(), "owns the tables") {
		t.Errorf("the owner was accepted as the indexer's role: %v", err)
	}
}

// The cursor and the rows are one transaction, and a cursor never goes
// backwards: two observers racing over a prefix leave it at the further one.
func TestTheCursorMovesWithItsRowsAndNeverBackwards(t *testing.T) {
	pool := pgtest.Open(t)
	ctx := context.Background()
	idx, err := postgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	corpus := indextest.Corpus(t)
	rows := make([]index.Row, 0, len(corpus))
	for _, p := range corpus {
		rows = append(rows, index.RowOf(p.Record, index.ObjectAt{Key: p.Key(), Line: p.Line}, indextest.Fields))
	}

	if err := idx.Advance(ctx, indextest.Profile, "acme", "k2", rows); err != nil {
		t.Fatal(err)
	}
	if err := idx.Advance(ctx, indextest.Profile, "acme", "k1", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := idx.Cursor(ctx, indextest.Profile, "acme"); got != "k2" {
		t.Errorf("the cursor went back to %q", got)
	}
	if got, _ := idx.Cursor(ctx, indextest.Profile, "globex"); got != "" {
		t.Errorf("a tenant that was never read has the cursor %q", got)
	}

	// A batch that fails leaves no cursor behind: the rows and the cursor are
	// one transaction. A row with no id cannot be inserted.
	bad := append([]index.Row{}, rows[0])
	bad[0].ID = "not-a-uuid"
	if err := idx.Advance(ctx, indextest.Profile, "initech", "k9", bad); err == nil {
		t.Fatal("a row that cannot be indexed was accepted")
	}
	if got, _ := idx.Cursor(ctx, indextest.Profile, "initech"); got != "" {
		t.Errorf("the cursor moved to %q though its rows were refused", got)
	}
}
