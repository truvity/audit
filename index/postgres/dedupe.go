package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Dedupe is the shared record of what has already been written.
//
// With the deduplication in one process, a repeat is only absorbed by the
// replica that saw the original, so the writer refuses to start more than one.
// This table is what lifts the refusal: two replicas consulting it agree about
// what has been written, and a redelivery landing on either is absorbed.
//
// Asking and marking are separate, and the writer marks only once the copies
// are durable. See the Dedupe interface in the writer for why that order is the
// one an audit trail can afford.
type Dedupe struct {
	DB DB
	// Window is how long an identifier is remembered. Default 14 days. It wants
	// to be at least as long as the longest redelivery a deployment's stream
	// permits, or a repeat arrives after the memory of it has gone.
	Window time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
}

// NewDedupe returns a deduplication store over a pool.
func NewDedupe(db DB, window time.Duration) (*Dedupe, error) {
	if db == nil {
		return nil, errors.New("postgres: a database is required")
	}
	return &Dedupe{DB: db, Window: window}, nil
}

// Seen implements the writer's Dedupe. It reads and marks nothing.
func (d *Dedupe) Seen(ctx context.Context, ids []string) (map[string]bool, error) {
	wanted := distinct(ids)
	if len(wanted) == 0 {
		return map[string]bool{}, nil
	}
	rows, err := d.DB.Query(ctx,
		`select id from seen where id = any($1) and seen_at >= $2`,
		wanted, d.now().Add(-d.window()))
	if err != nil {
		return nil, fmt.Errorf("postgres: deduplication: %w", err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("postgres: deduplication: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: deduplication: %w", err)
	}
	return out, nil
}

// Mark implements the writer's Dedupe.
//
// The timestamp is refreshed on a repeat so that an identifier stays remembered
// for a window from the last time it was written, not from the first.
func (d *Dedupe) Mark(ctx context.Context, ids []string) error {
	marked := distinct(ids)
	if len(marked) == 0 {
		return nil
	}
	_, err := d.DB.Exec(ctx, `
		insert into seen (id, seen_at)
		select unnest($1::text[]), $2::timestamptz
		on conflict (id) do update set seen_at = excluded.seen_at`,
		marked, d.now())
	if err != nil {
		return fmt.Errorf("postgres: marking %d records as written: %w", len(marked), err)
	}
	return nil
}

// Purge forgets identifiers marked before a time. A deployment runs it on a
// schedule; the table is otherwise unbounded.
func (d *Dedupe) Purge(ctx context.Context, before time.Time) error {
	if _, err := d.DB.Exec(ctx, `delete from seen where seen_at < $1`, before.UTC()); err != nil {
		return fmt.Errorf("postgres: purging the deduplication table: %w", err)
	}
	return nil
}

// distinct drops the blanks and the repeats. The repeats matter: an upsert of
// the same key twice in one statement is an error in Postgres, and a batch
// carrying a redelivery beside its original is an ordinary shape.
func distinct(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (d *Dedupe) window() time.Duration {
	if d.Window > 0 {
		return d.Window
	}
	return 14 * 24 * time.Hour
}

func (d *Dedupe) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}
