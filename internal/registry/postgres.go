package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/truvity/audit/index/postgres"
)

// Postgres keeps catalogues in the same database as the index.
//
// The same database and the same migration chain: a deployment that had to run
// two migrations in the right order would eventually run them in the wrong one,
// and every service here refuses to start on a schema version it does not know
// rather than migrating itself.
type Postgres struct{ DB postgres.DB }

// Put implements Store.
func (p Postgres) Put(ctx context.Context, e Entry) error {
	schemas, err := json.Marshal(asStrings(e.Schemas))
	if err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	_, err = p.DB.Exec(ctx, `
		insert into catalogues (source, version, document, schemas, registered_at, registered_by)
		values ($1,$2,$3,$4,$5,$6)
		on conflict (source, version) do nothing`,
		e.Source, e.Version, e.Document, schemas, e.RegisteredAt, e.RegisteredBy)
	if err != nil {
		return fmt.Errorf("registry: storing %s %s: %w", e.Source, e.Version, err)
	}
	return nil
}

// Get implements Store.
func (p Postgres) Get(ctx context.Context, source, version string) (Entry, error) {
	var e Entry
	var schemas []byte
	err := p.DB.QueryRow(ctx, `
		select source, version, document, schemas, registered_at, registered_by
		  from catalogues where source = $1 and version = $2`, source, version).
		Scan(&e.Source, &e.Version, &e.Document, &schemas, &e.RegisteredAt, &e.RegisteredBy)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Entry{}, fmt.Errorf("%w: %s %s", ErrNotFound, source, version)
	case err != nil:
		return Entry{}, fmt.Errorf("registry: %s %s: %w", source, version, err)
	}
	if e.Schemas, err = asBytes(schemas); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// List implements Store.
func (p Postgres) List(ctx context.Context) ([]Entry, error) {
	rows, err := p.DB.Query(ctx, `
		select source, version, document, schemas, registered_at, registered_by
		  from catalogues order by source, registered_at desc`)
	if err != nil {
		return nil, fmt.Errorf("registry: listing: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		var schemas []byte
		if err := rows.Scan(&e.Source, &e.Version, &e.Document, &schemas,
			&e.RegisteredAt, &e.RegisteredBy); err != nil {
			return nil, fmt.Errorf("registry: listing: %w", err)
		}
		if e.Schemas, err = asBytes(schemas); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("registry: listing: %w", err)
	}
	return out, nil
}

// The schemas are JSON documents, so they are kept as JSON rather than as
// opaque bytes: a deployment looking at this table should be able to read them.
func asStrings(m map[string][]byte) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = string(v)
	}
	return out
}

func asBytes(raw []byte) (map[string][]byte, error) {
	if len(raw) == 0 {
		return map[string][]byte{}, nil
	}
	var text map[string]string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	out := make(map[string][]byte, len(text))
	for k, v := range text {
		out[k] = []byte(v)
	}
	return out, nil
}

// Memory keeps catalogues in one process, for tests and for a deployment small
// enough to register on every start-up.
type Memory struct{ entries map[string]Entry }

// Put implements Store.
func (m *Memory) Put(_ context.Context, e Entry) error {
	if m.entries == nil {
		m.entries = map[string]Entry{}
	}
	m.entries[e.Source+"@"+e.Version] = e
	return nil
}

// Get implements Store.
func (m *Memory) Get(_ context.Context, source, version string) (Entry, error) {
	e, ok := m.entries[source+"@"+version]
	if !ok {
		return Entry{}, fmt.Errorf("%w: %s %s", ErrNotFound, source, version)
	}
	return e, nil
}

// List implements Store.
func (m *Memory) List(_ context.Context) ([]Entry, error) {
	out := make([]Entry, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, e)
	}
	return out, nil
}
