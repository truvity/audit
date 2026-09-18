// Package postgres is the default index and the shared deduplication store.
//
// It holds two things that look unrelated and are not: the projection a search
// reads, and the table that lets several writer replicas agree about what has
// already been written. Both sit on the write path, and putting them in one
// database is what turns the writer from a single instance into a deployment.
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
const Version = 5

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
	if len(rows) == 0 {
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

	fresh, err := insertCore(ctx, tx, profile, rows)
	if err != nil {
		return err
	}
	if len(fresh) == 0 {
		// Every row was already there. Committing an empty transaction is
		// cheaper than reasoning about whether it matters.
		return tx.Commit(ctx)
	}
	if err := insertContext(ctx, tx, profile, rows, fresh); err != nil {
		return err
	}
	if err := insertData(ctx, tx, profile, rows, fresh); err != nil {
		return err
	}
	if err := addCounts(ctx, tx, profile, rows, fresh); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: committing %d rows: %w", len(rows), err)
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
// Doing it from the write path rather than from a schedule means a writer that
// runs over a month boundary at three in the morning does not stop, and a
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

	from, to := month.Format("2006-01-02"), month.AddDate(0, 1, 0).Format("2006-01-02")
	for _, table := range []string{"events_core", "events_context", "events_data"} {
		statement := fmt.Sprintf(
			`create table if not exists %s_%s partition of %s for values from ('%s') to ('%s')`,
			table, suffix, table, from, to)
		if _, err := i.DB.Exec(ctx, statement); err != nil {
			// Two writers creating the same partition at the same moment is
			// normal; one of them loses and the partition exists either way.
			if exists(ctx, i.DB, table+"_"+suffix) {
				continue
			}
			return fmt.Errorf("postgres: creating partition %s_%s: %w", table, suffix, err)
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
