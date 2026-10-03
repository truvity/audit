// Package postgres is the default index and the shared deduplication store.
//
// It holds two things that look unrelated: the projection a search reads, which
// observe writes by cursor (docs/decisions/0020), and the table that lets
// several writer replicas agree about what has already been written. They share
// a database and one migration chain, and nothing else: the writer's role may
// touch the deduplication table and the registry and not the index, observe's
// the index and not the deduplication table, and the query service's only reads
// it (GrantWriter, GrantObserver, GrantReader).
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/truvity/audit/index"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// migrations are the numbered files, applied in name order. They share one
// chain across everything that uses this database — the index, the registry —
// because a deployment that had to run two migrations in the right order would
// eventually run them in the wrong one.
func migrations() ([]string, error) {
	names, err := fs.Glob(schemaFS, "schema/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// Version is the schema this build expects. A writer whose database is at a
// different version refuses to start rather than guess: migrating from several
// replicas at once is a race, so the migration is its own step and this is the
// check that it ran.
const Version = 6

// Schema returns the migrations in order, so that a deployment can apply them
// with whatever it already uses rather than through this code.
func Schema() string {
	names, err := migrations()
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, name := range names {
		body, err := schemaFS.ReadFile(name)
		if err != nil {
			return ""
		}
		fmt.Fprintf(&b, "-- %s\n%s\n", name, body)
	}
	return b.String()
}

// DB is the part of a pgx pool this package uses. Taking an interface keeps the
// pool's construction — its size, its timeouts, its credentials — where a
// deployment can see it.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Migrate applies the schema. It is idempotent, and it is meant to be run by
// one thing at a time: a job before the writers roll, or an operator.
func Migrate(ctx context.Context, db DB) error {
	names, err := migrations()
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	for _, name := range names {
		body, err := schemaFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("postgres: %w", err)
		}
		if _, err := db.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("postgres: applying %s: %w", name, err)
		}
	}
	return nil
}

// The tables each part may touch. Three groups, because there are three
// jobs and no part does two of them: the index (observe writes it, a query
// service reads it), the write path's own state (the deduplication table, the
// registry and the key directory), and the version row every part checks at
// start-up.
var (
	indexTables  = []string{"events_core", "events_context", "events_data", "facet_counts", "index_cursor"}
	writerTables = []string{"seen", "catalogues", "audit_key_directory"}
	// rlsTables are the ones row-level security is enabled on. A role that
	// writes them needs a policy of its own, because the tenant policies read
	// a per-request setting and a writer of every tenant's rows has none.
	rlsTables = []string{"events_core", "facet_counts"}
)

// Roles are the database roles of a deployment, one per part. Each is optional:
// a deployment that runs one part under the owner's own role names none, and
// gets no separation. Every role must already exist.
type Roles struct {
	// Writer is the write path's: the deduplication table, the catalogue
	// registry and the key directory, and nothing of the index.
	Writer string
	// Observe is the indexer's: it reads and writes the index and its cursors,
	// and creates monthly partitions through one function.
	Observe string
	// Reader is the query service's: select on the index, bound by row-level
	// security to the tenants of each request.
	Reader string
	// Purge is the retention job's: it forgets index rows and old
	// deduplication entries.
	Purge string
}

// GrantRoles grants each named role what its part needs and takes from it
// everything else on this schema's tables. Run it as the owner, after Migrate.
//
// It is the whole of the separation between the parts at the database. The
// writer, observe and the query service connect as three different roles, none
// of them the owner, so that a writer that is compromised cannot rewrite what
// search answers with, an observer cannot mark a record as already written, and
// a reader can do neither.
func GrantRoles(ctx context.Context, db DB, r Roles) error {
	named := map[string]string{}
	for part, role := range map[string]string{"writer": r.Writer, "observe": r.Observe, "reader": r.Reader, "purge": r.Purge} {
		if role == "" {
			continue
		}
		if other, dup := named[role]; dup {
			return fmt.Errorf("postgres: %s is the role of both %s and %s: the separation is that they are different", role, other, part)
		}
		named[role] = part
	}
	if r.Reader != "" {
		if err := GrantReader(ctx, db, r.Reader); err != nil {
			return err
		}
	}
	if r.Writer != "" {
		if err := GrantWriter(ctx, db, r.Writer); err != nil {
			return err
		}
	}
	if r.Observe != "" {
		if err := GrantObserver(ctx, db, r.Observe); err != nil {
			return err
		}
	}
	if r.Purge != "" {
		if err := GrantPurger(ctx, db, r.Purge); err != nil {
			return err
		}
	}
	return nil
}

// grantTo applies statements for a role, after checking the role is not the
// owner of the tables: an owner is not bound by a grant, so naming it would
// only look like separation.
func grantTo(ctx context.Context, db DB, role, what string, statements func(role, schema string) []string) error {
	if strings.TrimSpace(role) == "" {
		return fmt.Errorf("postgres: a %s role is required", what)
	}
	var schema, owner string
	if err := db.QueryRow(ctx, `select current_schema(), current_user`).Scan(&schema, &owner); err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	if role == owner {
		return fmt.Errorf("postgres: %s owns the tables, and the %s role must not: "+
			"an owner is bound by no grant and by no row-level security", role, what)
	}
	id, in := pgx.Identifier{role}.Sanitize(), pgx.Identifier{schema}.Sanitize()
	for _, statement := range statements(id, in) {
		if _, err := db.Exec(ctx, statement); err != nil {
			return fmt.Errorf("postgres: granting %s the %s's rights: %w", role, what, err)
		}
	}
	return nil
}

func qualified(schema string, tables []string) string {
	names := make([]string, len(tables))
	for n, t := range tables {
		names[n] = schema + "." + pgx.Identifier{t}.Sanitize()
	}
	return strings.Join(names, ", ")
}

// policyOn gives a role that writes every tenant's rows a policy of its own on
// a table with row-level security.
func policyOn(table, part, role string) []string {
	name := pgx.Identifier{table + "_" + part}.Sanitize()
	return []string{
		"drop policy if exists " + name + " on " + table,
		"create policy " + name + " on " + table + " for all to " + role + " using (true) with check (true)",
	}
}

// GrantReader gives a role what a reading service needs and nothing more:
// usage on the index's schema, and select on the index's tables, which a month's
// partition inherits from its parent. Run it as the owner, after Migrate.
//
// The role must exist, and must not be the owner: row-level security does not
// apply to the role that owns the tables, so tenant isolation for a reader that
// owned them would rest on the service alone.
func GrantReader(ctx context.Context, db DB, role string) error {
	return grantTo(ctx, db, role, "reader", func(id, in string) []string {
		return []string{
			"grant usage on schema " + in + " to " + id,
			// Whatever an earlier version of this grant gave: select on every
			// table in the schema, and on those made later.
			"revoke all on all tables in schema " + in + " from " + id,
			"alter default privileges in schema " + in + " revoke select on tables from " + id,
			"grant select on " + qualified(in, indexTables[:4]) + " to " + id,
			"grant select on " + in + ".audit_schema_version to " + id,
		}
	})
}

// GrantWriter gives a role what the write path needs: the deduplication table,
// the catalogue registry and the key directory, and the schema version it
// checks at start-up. It gives nothing of the index, and takes back anything an
// earlier version of the deployment had granted: the writer does not index.
func GrantWriter(ctx context.Context, db DB, role string) error {
	return grantTo(ctx, db, role, "writer", func(id, in string) []string {
		return []string{
			"grant usage on schema " + in + " to " + id,
			"revoke all on " + qualified(in, indexTables) + " from " + id,
			"grant select, insert, update, delete on " + qualified(in, writerTables) + " to " + id,
			"grant select on " + in + ".audit_schema_version to " + id,
		}
	})
}

// GrantObserver gives a role what the indexer needs: the index and its
// cursors, and the one function that creates a month's partitions, because the
// role is not the owner and creating a partition takes the owner. It gets
// nothing of the deduplication table or the registry.
func GrantObserver(ctx context.Context, db DB, role string) error {
	return grantTo(ctx, db, role, "observe", func(id, in string) []string {
		out := []string{
			"grant usage on schema " + in + " to " + id,
			"revoke all on " + qualified(in, writerTables) + " from " + id,
			"grant select, insert on " + qualified(in, indexTables[:3]) + " to " + id,
			"grant select, insert, update on " + in + ".facet_counts to " + id,
			"grant select, insert, update, delete on " + in + ".index_cursor to " + id,
			"grant select on " + in + ".audit_schema_version to " + id,
			"grant execute on function " + in + ".audit_ensure_month(date) to " + id,
		}
		for _, table := range rlsTables {
			out = append(out, policyOn(table, "observe", id)...)
		}
		return out
	})
}

// GrantPurger gives a role what the retention job needs: to forget index rows
// and cursors, and old deduplication entries. It cannot add a row to either.
func GrantPurger(ctx context.Context, db DB, role string) error {
	return grantTo(ctx, db, role, "purge", func(id, in string) []string {
		out := []string{
			"grant usage on schema " + in + " to " + id,
			"grant select, update, delete on " + qualified(in, indexTables[:4]) + " to " + id,
			"grant select, delete on " + in + ".seen to " + id,
			"grant select on " + in + ".audit_schema_version to " + id,
		}
		for _, table := range rlsTables {
			out = append(out, policyOn(table, "purge", id)...)
		}
		return out
	})
}

// CheckVersion reports whether the database holds the schema this build knows.
func CheckVersion(ctx context.Context, db DB) error {
	var version int
	err := db.QueryRow(ctx, `select coalesce(max(version), 0) from audit_schema_version`).Scan(&version)
	if err != nil {
		return fmt.Errorf(
			"postgres: reading the schema version: %w; run `audit migrate` before starting a writer", err)
	}
	if version != Version {
		return fmt.Errorf(
			"postgres: the database is at schema version %d and this build expects %d; "+
				"run `audit migrate`", version, Version)
	}
	return nil
}

// Index is the projection.
type Index struct {
	DB DB
	// Partitions is how far ahead a partition is created when one is needed.
	// Zero means the month itself only.
	Partitions int

	mu      sync.Mutex
	ensured map[string]bool
	// reader pins every read; see NewReader.
	reader bool
}

// New returns an index over a pool.
func New(db DB) (*Index, error) {
	if db == nil {
		return nil, errors.New("postgres: a database is required")
	}
	return &Index{DB: db, ensured: map[string]bool{}}, nil
}

// Index implements index.Indexer.
//
// The whole batch is one transaction, and the facet counts move only for the
// rows the insert actually created. That is why counting is not a call of its
// own: a caller cannot tell a re-delivered record from a new one, and only the
// transaction that inserted the row can.
func (i *Index) Index(ctx context.Context, profile string, rows []index.Row) error {
	return i.write(ctx, profile, rows, nil)
}

// Advance is what observe indexes with: the rows, and the cursor of the prefix
// they came from, in one transaction. A crash leaves both or neither, which is
// what lets a restarted observe resume from the cursor without a row missing
// behind it. The rows may be empty: a stretch of the listing that holds nothing
// to index still moves the cursor.
func (i *Index) Advance(ctx context.Context, profile, tenant, lastKey string, rows []index.Row) error {
	return i.write(ctx, profile, rows, &cursor{tenant: tenant, key: lastKey})
}

type cursor struct{ tenant, key string }

func (i *Index) write(ctx context.Context, profile string, rows []index.Row, to *cursor) error {
	if len(rows) == 0 && to == nil {
		return nil
	}
	if err := i.ensurePartitions(ctx, rows); err != nil {
		return err
	}

	tx, err := i.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a committed transaction rolls back to nothing

	if len(rows) > 0 {
		fresh, err := insertCore(ctx, tx, profile, rows)
		if err != nil {
			return err
		}
		// When every row was already there there is nothing more to write;
		// the cursor, if any, still moves.
		if len(fresh) > 0 {
			if err := insertContext(ctx, tx, profile, rows, fresh); err != nil {
				return err
			}
			if err := insertData(ctx, tx, profile, rows, fresh); err != nil {
				return err
			}
			if err := addCounts(ctx, tx, profile, rows, fresh); err != nil {
				return err
			}
		}
	}
	if to != nil {
		// Never backwards: two observers that raced each other over one prefix
		// must leave the cursor at the further of the two.
		if _, err := tx.Exec(ctx, `
			insert into index_cursor (profile, tenant_id, last_key, updated_at)
			values ($1, $2, $3, now())
			on conflict (profile, tenant_id) do update
			   set last_key = excluded.last_key, updated_at = now()
			 where index_cursor.last_key < excluded.last_key`,
			profile, to.tenant, to.key); err != nil {
			return fmt.Errorf("postgres: moving the cursor of %s/%s: %w", profile, to.tenant, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: committing %d rows: %w", len(rows), err)
	}
	return nil
}

// Cursor is the key of the last object indexed under a profile and tenant, or
// "" when none has been.
func (i *Index) Cursor(ctx context.Context, profile, tenant string) (string, error) {
	var key string
	err := i.DB.QueryRow(ctx,
		`select last_key from index_cursor where profile = $1 and tenant_id = $2`, profile, tenant).Scan(&key)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("postgres: reading the cursor of %s/%s: %w", profile, tenant, err)
	}
	return key, nil
}

// ResetCursor forgets where a profile's tenant was read to, or every tenant of
// the profile when tenant is empty, so that observe reads it again from the
// start. The index keeps what it has: indexing is idempotent, and a row that is
// there is left alone.
func (i *Index) ResetCursor(ctx context.Context, profile, tenant string) error {
	var err error
	if tenant == "" {
		_, err = i.DB.Exec(ctx, `delete from index_cursor where profile = $1`, profile)
	} else {
		_, err = i.DB.Exec(ctx, `delete from index_cursor where profile = $1 and tenant_id = $2`, profile, tenant)
	}
	if err != nil {
		return fmt.Errorf("postgres: resetting the cursor of %s: %w", profile, err)
	}
	return nil
}

// insertCore writes the events and reports which of them were new.
func insertCore(ctx context.Context, tx pgx.Tx, profile string, rows []index.Row) (map[string]bool, error) {
	batch := &pgx.Batch{}
	for _, row := range rows {
		batch.Queue(`
			insert into events_core (
				profile, id, recorded_at, tenant_id, occurred_at, seq, source,
				action, operation, outcome, target_types, target_ids, object_key, line)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			on conflict do nothing
			returning id`,
			profile, row.ID, row.RecordedAt, row.TenantID, row.OccurredAt, int64(row.Sequence),
			row.Source, row.Action, row.Operation, row.Outcome,
			nonNil(row.TargetTypes), nonNil(row.TargetIDs), row.ObjectKey, row.Line)
	}
	results := tx.SendBatch(ctx, batch)
	defer results.Close() //nolint:errcheck // the error surfaces on the next statement

	fresh := make(map[string]bool, len(rows))
	for _, row := range rows {
		var id string
		switch err := results.QueryRow().Scan(&id); {
		case err == nil:
			fresh[row.ID] = true
		case errors.Is(err, pgx.ErrNoRows):
			// Already indexed. This is the normal case on a reindex and on a
			// re-delivery, and it must not move a count.
		default:
			return nil, fmt.Errorf("postgres: indexing %s: %w", row.ID, err)
		}
	}
	return fresh, results.Close()
}

func insertContext(ctx context.Context, tx pgx.Tx, profile string, rows []index.Row, fresh map[string]bool) error {
	batch := &pgx.Batch{}
	for _, row := range rows {
		if !fresh[row.ID] {
			continue
		}
		batch.Queue(`
			insert into events_context (
				profile, id, recorded_at, actor_kind, actor_id, subject_kind,
				subject_id, client_address, request_id, trace_id, observer_id)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			on conflict do nothing`,
			profile, row.ID, row.RecordedAt, row.ActorKind, row.ActorID, row.SubjectKind,
			row.SubjectID, row.ClientAddress, row.RequestID, row.TraceID, row.ObserverID)
	}
	return run(ctx, tx, batch, "context")
}

func insertData(ctx context.Context, tx pgx.Tx, profile string, rows []index.Row, fresh map[string]bool) error {
	batch := &pgx.Batch{}
	for _, row := range rows {
		if !fresh[row.ID] {
			continue
		}
		for _, v := range row.Data {
			var (
				text *string
				n    *int64
				at   *time.Time
			)
			switch v.Kind {
			case index.Int, index.Bool:
				value := v.Int
				n = &value
			case index.Time:
				value := v.At
				at = &value
				text = &v.Text
			default:
				text = &v.Text
			}
			batch.Queue(`
				insert into events_data (profile, id, recorded_at, path, kind, value_text, value_int, value_time)
				values ($1,$2,$3,$4,$5,$6,$7,$8)
				on conflict do nothing`,
				profile, row.ID, row.RecordedAt, v.Path, string(v.Kind), text, n, at)
		}
	}
	return run(ctx, tx, batch, "data")
}

// addCounts moves the facet counts, for the new rows only.
func addCounts(ctx context.Context, tx pgx.Tx, profile string, rows []index.Row, fresh map[string]bool) error {
	var deltas []index.FacetDelta
	for _, row := range rows {
		if fresh[row.ID] {
			deltas = append(deltas, row.Facets()...)
		}
	}
	batch := &pgx.Batch{}
	for _, d := range index.Merge(deltas) {
		batch.Queue(`
			insert into facet_counts (profile, tenant_id, hour, field, facet_value, total)
			values ($1,$2,$3,$4,$5,$6)
			on conflict (profile, tenant_id, hour, field, facet_value)
			do update set total = facet_counts.total + excluded.total`,
			profile, d.TenantID, d.Hour, d.Field, d.Value, d.Count)
	}
	return run(ctx, tx, batch, "counts")
}

func run(ctx context.Context, tx pgx.Tx, batch *pgx.Batch, what string) error {
	if batch.Len() == 0 {
		return nil
	}
	results := tx.SendBatch(ctx, batch)
	if err := results.Close(); err != nil {
		return fmt.Errorf("postgres: writing %s: %w", what, err)
	}
	return nil
}

// Purge implements index.Indexer.
//
// Identifying forgets who, and keeps what happened; Everything drops the month
// outright, which is what makes an expired retention cheap. Neither touches the
// archive: those objects are under a lock, and the profile's own retention is
// what releases them.
func (i *Index) Purge(ctx context.Context, profile string, before time.Time, what index.Scope) error {
	before = before.UTC()
	if what == index.Everything {
		for _, table := range []string{"events_data", "events_context", "events_core"} {
			_, err := i.DB.Exec(ctx,
				fmt.Sprintf(`delete from %s where profile = $1 and recorded_at < $2`, table),
				profile, before)
			if err != nil {
				return fmt.Errorf("postgres: purging %s: %w", table, err)
			}
		}
		_, err := i.DB.Exec(ctx,
			`delete from facet_counts where profile = $1 and hour < $2`, profile, before)
		if err != nil {
			return fmt.Errorf("postgres: purging counts: %w", err)
		}
		return nil
	}
	_, err := i.DB.Exec(ctx, `
		update events_context
		   set actor_id = '', subject_id = '', client_address = '',
		       request_id = '', trace_id = ''
		 where profile = $1 and recorded_at < $2
		   and (actor_id <> '' or subject_id <> '' or client_address <> ''
		        or request_id <> '' or trace_id <> '')`, profile, before)
	if err != nil {
		return fmt.Errorf("postgres: purging the identifying columns: %w", err)
	}
	return nil
}

// ensurePartitions creates the monthly partitions the batch needs.
//
// Doing it from the indexing path rather than from a schedule means an observer
// that runs over a month boundary at three in the morning does not stop, and a
// deployment that forgot the job does not lose its index.
func (i *Index) ensurePartitions(ctx context.Context, rows []index.Row) error {
	months := map[time.Time]bool{}
	for _, row := range rows {
		at := row.RecordedAt.UTC()
		months[time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)] = true
	}
	ordered := make([]time.Time, 0, len(months))
	for m := range months {
		ordered = append(ordered, m)
	}
	sort.Slice(ordered, func(a, b int) bool { return ordered[a].Before(ordered[b]) })

	for _, month := range ordered {
		for ahead := 0; ahead <= i.Partitions; ahead++ {
			if err := i.ensureMonth(ctx, month.AddDate(0, ahead, 0)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (i *Index) ensureMonth(ctx context.Context, month time.Time) error {
	suffix := month.Format("2006_01")
	i.mu.Lock()
	done := i.ensured[suffix]
	i.mu.Unlock()
	if done {
		return nil
	}

	// The partitions are made by a function the migration created, which runs as
	// the tables' owner: this process is not the owner and may not create them.
	if _, err := i.DB.Exec(ctx, `select audit_ensure_month($1::date)`, month.Format("2006-01-02")); err != nil {
		// Two observers creating the same partition at the same moment is
		// normal; one of them loses and the partition exists either way.
		for _, table := range []string{"events_core", "events_context", "events_data"} {
			if !exists(ctx, i.DB, table+"_"+suffix) {
				return fmt.Errorf("postgres: creating partition %s_%s: %w", table, suffix, err)
			}
		}
	}
	i.mu.Lock()
	i.ensured[suffix] = true
	i.mu.Unlock()
	return nil
}

func exists(ctx context.Context, db DB, table string) bool {
	var found bool
	if err := db.QueryRow(ctx, `select to_regclass($1) is not null`, table).Scan(&found); err != nil {
		return false
	}
	return found
}

// nonNil keeps a nil slice out of a not-null column.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
