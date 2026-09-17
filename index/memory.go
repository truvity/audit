package index

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Memory holds the projection in one process.
//
// It is what the tests index into and what a deployment small enough to keep
// its index in memory uses. It is a real implementation, not a stub: it
// enforces the same idempotency the interface promises, because a memory
// implementation that quietly double-counts would let a fault reach Postgres
// before anybody noticed.
type Memory struct {
	mu      sync.RWMutex
	rows    map[string]map[string]Row // profile -> id -> row
	counts  map[string]map[countKey]int64
	Indexed int
}

type countKey struct {
	tenant, field, value string
	hour                 time.Time
}

// NewMemory returns an empty index.
func NewMemory() *Memory {
	return &Memory{
		rows:   map[string]map[string]Row{},
		counts: map[string]map[countKey]int64{},
	}
}

// Index implements Indexer.
func (m *Memory) Index(_ context.Context, profile string, rows []Row) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows[profile] == nil {
		m.rows[profile] = map[string]Row{}
		m.counts[profile] = map[countKey]int64{}
	}

	var deltas []FacetDelta
	for _, row := range rows {
		if _, already := m.rows[profile][row.ID]; already {
			// The same copy arriving twice is the normal case, not an error:
			// every hop below the writer is at-least-once. What must not happen
			// is for it to be counted twice.
			continue
		}
		m.rows[profile][row.ID] = row
		m.Indexed++
		deltas = append(deltas, row.Facets()...)
	}
	for _, d := range Merge(deltas) {
		m.counts[profile][countKey{d.TenantID, d.Field, d.Value, d.Hour}] += d.Count
	}
	return nil
}

// Purge implements Indexer.
func (m *Memory) Purge(_ context.Context, profile string, before time.Time, what Scope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, row := range m.rows[profile] {
		if !row.RecordedAt.Before(before) {
			continue
		}
		if what == Everything {
			delete(m.rows[profile], id)
			continue
		}
		// The event stays and says what happened; who it happened to does not.
		row.ActorID, row.SubjectID, row.ClientAddress = "", "", ""
		row.RequestID, row.TraceID = "", ""
		m.rows[profile][id] = row
	}
	return nil
}

// Row returns one row, for tests and for the memory searcher.
func (m *Memory) Row(profile, id string) (Row, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	row, ok := m.rows[profile][id]
	return row, ok
}

// Rows returns a profile's rows in recorded order, which is the order a tail
// advances in.
func (m *Memory) Rows(profile string) []Row {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Row, 0, len(m.rows[profile]))
	for _, row := range m.rows[profile] {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RecordedAt.Equal(out[j].RecordedAt) {
			return out[i].RecordedAt.Before(out[j].RecordedAt)
		}
		if out[i].Sequence != out[j].Sequence {
			return out[i].Sequence < out[j].Sequence
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Count returns one facet count.
func (m *Memory) Count(profile, tenant, field, value string, hour time.Time) int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.counts[profile][countKey{tenant, field, value, hour.UTC().Truncate(time.Hour)}]
}

// Counts returns every count of a profile, for tests and for the reindex
// report.
func (m *Memory) Counts(profile string) []FacetDelta {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]FacetDelta, 0, len(m.counts[profile]))
	for k, n := range m.counts[profile] {
		out = append(out, FacetDelta{
			TenantID: k.tenant, Hour: k.hour, Field: k.field, Value: k.value, Count: n,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].less(out[j]) })
	return out
}

// Len is how many rows a profile has.
func (m *Memory) Len(profile string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.rows[profile])
}
