package index

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Capabilities implements Searcher.
//
// The memory searcher exists so that the interface has two honest
// implementations from the start. Two is the number that keeps an interface
// from being a description of one implementation's habits.
func (m *Memory) Capabilities() Capabilities {
	return Capabilities{
		FreeText:        false,
		Facets:          true,
		DataPredicates:  true,
		MaxConjunctions: 4,
		SortFields: []string{
			SortOccurredAt, SortRecordedAt, SortID, SortAction, SortTenantID, SortSource,
		},
	}
}

// Search implements Searcher.
func (m *Memory) Search(_ context.Context, q Query) (Page, error) {
	if q.Profile == "" {
		return Page{}, fmt.Errorf("index: a search names one profile")
	}
	if most := m.Capabilities().MaxConjunctions; len(q.Filter) > most {
		return Page{}, fmt.Errorf(
			"index: %d conjunctions, and %d is the most this searcher will take", len(q.Filter), most)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	order := searchOrder(q.Sort)

	var matched []Row
	for _, r := range m.Rows(q.Profile) {
		if !granted(r, q.Tenants) || !Matches(r, q.Filter) {
			continue
		}
		matched = append(matched, r)
	}
	sortRows(matched, order)

	// The boundary is applied after sorting, as the database applies it in the
	// same order it reads: a keyset resumes from a position in the ordering,
	// not from a position in the data.
	if q.After != nil {
		matched = after(matched, order, q.After, q.Backwards)
	}
	if q.Backwards {
		for a, z := 0, len(matched)-1; a < z; a, z = a+1, z-1 {
			matched[a], matched[z] = matched[z], matched[a]
		}
	}

	page := Page{}
	if len(matched) > limit {
		page.More = true
		matched = matched[:limit]
	}
	if q.Backwards {
		for a, z := 0, len(matched)-1; a < z; a, z = a+1, z-1 {
			matched[a], matched[z] = matched[z], matched[a]
		}
	}
	page.Rows = matched
	if n := len(page.Rows); n > 0 {
		page.Next = boundary(page.Rows[n-1], order)
		page.Prev = boundary(page.Rows[0], order)
	} else if q.After != nil {
		page.Next = q.After
	}
	return page, nil
}

// Facets implements Searcher.
func (m *Memory) Facets(_ context.Context, q Query, fields []string, limit int) ([]Facet, error) {
	if limit <= 0 {
		limit = 20
	}
	out := make([]Facet, 0, len(fields))
	for _, field := range fields {
		counts := map[string]int64{}
		for _, r := range m.Rows(q.Profile) {
			if !granted(r, q.Tenants) || !Matches(r, q.Filter) {
				continue
			}
			// A record naming one value twice counts once, as the counts do:
			// the question is how many records carried it.
			seen := map[string]bool{}
			for _, d := range r.Facets() {
				if d.Field == field && !seen[d.Value] {
					seen[d.Value] = true
					counts[d.Value]++
				}
			}
		}
		values := make([]FacetValue, 0, len(counts))
		for v, n := range counts {
			values = append(values, FacetValue{Value: v, Count: n})
		}
		sort.Slice(values, func(a, b int) bool {
			if values[a].Count != values[b].Count {
				return values[a].Count > values[b].Count
			}
			return values[a].Value < values[b].Value
		})
		if len(values) > limit {
			values = values[:limit]
		}
		out = append(out, Facet{Field: field, Values: values})
	}
	return out, nil
}

// Get implements Searcher.
func (m *Memory) Get(_ context.Context, profile, id string) (Row, Provenance, error) {
	r, ok := m.Row(profile, id)
	if !ok {
		return Row{}, Provenance{}, fmt.Errorf("index: no record %s in profile %s", id, profile)
	}
	return r, Provenance{ObjectKey: r.ObjectKey, Line: r.Line}, nil
}

func granted(r Row, tenants []string) bool {
	if len(tenants) == 0 {
		return true
	}
	for _, t := range tenants {
		if r.TenantID == t {
			return true
		}
	}
	return false
}

// Matches reports whether a row satisfies a filter: the OR of conjunctions,
// each of which is an AND of predicates. No filter matches everything.
//
// It is exported because it is the definition of what a query means, and a
// searcher that worked it out again would be a second opinion on the contract.
// Anything implementing Searcher over data it holds itself should use this
// rather than reimplement the operators.
func Matches(r Row, filter []Conjunction) bool {
	if len(filter) == 0 {
		return true
	}
	for _, c := range filter {
		if holds(r, c) {
			return true
		}
	}
	return false
}

func holds(r Row, c Conjunction) bool {
	text := []struct {
		value string
		on    []Predicate
	}{
		{r.ID, c.ID}, {r.Source, c.Source}, {r.Action, c.Action},
		{r.Operation, c.Operation}, {r.Outcome, c.Outcome}, {r.TenantID, c.TenantID},
		{r.ActorKind, c.ActorKind}, {r.ActorID, c.ActorID},
		{r.SubjectKind, c.SubjectKind}, {r.SubjectID, c.SubjectID},
		{r.RequestID, c.RequestID}, {r.TraceID, c.TraceID},
		{r.ClientAddress, c.ClientAddress}, {r.ObserverID, c.ObserverID},
	}
	for _, t := range text {
		for _, p := range t.on {
			if !compare(t.value, p) {
				return false
			}
		}
	}
	for _, w := range c.OccurredAt {
		if !within(r.OccurredAt, w) {
			return false
		}
	}
	for _, w := range c.RecordedAt {
		if !within(r.RecordedAt, w) {
			return false
		}
	}
	for _, p := range c.TargetType {
		if !anyOf(r.TargetTypes, p) {
			return false
		}
	}
	for _, p := range c.TargetID {
		if !anyOf(r.TargetIDs, p) {
			return false
		}
	}
	for _, p := range c.Data {
		if !hasData(r, p) {
			return false
		}
	}
	return true
}

func compare(value string, p Predicate) bool {
	switch p.Op {
	case Equal:
		return value == p.Value
	case NotEqual:
		return value != p.Value
	case In:
		return contains(p.Values, value)
	case NotIn:
		return !contains(p.Values, value)
	case Prefix:
		return strings.HasPrefix(value, p.Value)
	default:
		return false
	}
}

func anyOf(values []string, p Predicate) bool {
	switch p.Op {
	case Equal:
		return contains(values, p.Value)
	case NotEqual:
		return !contains(values, p.Value)
	case In:
		for _, v := range values {
			if contains(p.Values, v) {
				return true
			}
		}
		return false
	case NotIn:
		for _, v := range values {
			if contains(p.Values, v) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func hasData(r Row, p PathPredicate) bool {
	for _, v := range r.Data {
		if v.Path != p.Path {
			continue
		}
		switch p.Kind {
		case Int, Bool:
			if v.Int == p.Int {
				return true
			}
		case Time:
			if v.At.Equal(p.At) {
				return true
			}
		default:
			if compare(v.Text, Predicate{Op: p.Op, Value: p.Text, Values: p.Values}) {
				return true
			}
		}
	}
	return false
}

// within is half-open, so adjacent ranges neither overlap nor leave a gap.
func within(at time.Time, w TimePredicate) bool {
	if !w.From.IsZero() && at.Before(w.From) {
		return false
	}
	if !w.To.IsZero() && !at.Before(w.To) {
		return false
	}
	return true
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// searchOrder mirrors the database's: whatever was asked, ending in the
// identifier so that the ordering is total.
func searchOrder(sort []SortBy) []SortBy {
	out := make([]SortBy, 0, len(sort)+1)
	seen := map[string]bool{}
	for _, s := range sort {
		if s.Field == "" || seen[s.Field] {
			continue
		}
		seen[s.Field] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		out = append(out, SortBy{Field: SortOccurredAt, Descending: true})
	}
	if !seen[SortID] {
		out = append(out, SortBy{Field: SortID})
	}
	return out
}

func key(r Row, field string) string {
	switch field {
	case SortOccurredAt:
		return r.OccurredAt.UTC().Format(time.RFC3339Nano)
	case SortRecordedAt:
		return r.RecordedAt.UTC().Format(time.RFC3339Nano)
	case SortAction:
		return r.Action
	case SortTenantID:
		return r.TenantID
	case SortSource:
		return r.Source
	default:
		return r.ID
	}
}

func sortRows(rows []Row, order []SortBy) {
	sort.SliceStable(rows, func(a, b int) bool {
		for _, s := range order {
			x, y := key(rows[a], s.Field), key(rows[b], s.Field)
			if x == y {
				continue
			}
			if s.Descending {
				return x > y
			}
			return x < y
		}
		return false
	})
}

// after drops everything up to and including the boundary.
func after(rows []Row, order []SortBy, at *Boundary, backwards bool) []Row {
	want := append(append([]string{}, at.Values...), at.ID)
	var out []Row
	for _, r := range rows {
		beyond := false
		for i, s := range order {
			got := key(r, s.Field)
			if got == want[i] {
				continue
			}
			descending := s.Descending
			if backwards {
				descending = !descending
			}
			if descending {
				beyond = got < want[i]
			} else {
				beyond = got > want[i]
			}
			break
		}
		if beyond {
			out = append(out, r)
		}
	}
	return out
}

func boundary(r Row, order []SortBy) *Boundary {
	out := &Boundary{ID: r.ID, RecordedAt: r.RecordedAt, Sequence: r.Sequence}
	for _, s := range order[:len(order)-1] {
		out.Values = append(out.Values, key(r, s.Field))
	}
	return out
}
