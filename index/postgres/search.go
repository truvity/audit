package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/truvity/audit/index"
)

// Capabilities implements index.Searcher.
func (i *Index) Capabilities() index.Capabilities {
	return index.Capabilities{
		FreeText:        false,
		Facets:          true,
		DataPredicates:  true,
		MaxConjunctions: 4,
		SortFields: []string{
			index.SortOccurredAt, index.SortRecordedAt, index.SortID,
			index.SortAction, index.SortTenantID, index.SortSource,
		},
	}
}

// builder accumulates a statement and its arguments, so that every value a
// caller supplied reaches the database as a parameter and never as text spliced
// into SQL.
type builder struct {
	sql  strings.Builder
	args []any
}

func (b *builder) arg(v any) string {
	b.args = append(b.args, v)
	return fmt.Sprintf("$%d", len(b.args))
}

// Search implements index.Searcher.
func (i *Index) Search(ctx context.Context, q index.Query) (index.Page, error) {
	if i.reader {
		var page index.Page
		err := AsTenants(ctx, i.DB, PinOf(q.Tenants), func(pinned *Index) error {
			var err error
			page, err = pinned.Search(ctx, q)
			return err
		})
		return page, err
	}
	if q.Profile == "" {
		return index.Page{}, errors.New("postgres: a search names one profile")
	}
	if most := i.Capabilities().MaxConjunctions; len(q.Filter) > most {
		return index.Page{}, fmt.Errorf(
			"postgres: %d conjunctions, and %d is the most this searcher will take: "+
				"an unbounded OR is unbounded work on a table that only grows",
			len(q.Filter), most)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	order := sortOrder(q.Sort)

	b := &builder{}
	b.sql.WriteString(`
		select c.id, c.tenant_id, c.occurred_at, c.recorded_at, c.seq, c.source,
		       c.action, c.operation, c.outcome, c.target_types, c.target_ids,
		       c.object_key, c.line,
		       coalesce(x.actor_kind,''), coalesce(x.actor_id,''),
		       coalesce(x.subject_kind,''), coalesce(x.subject_id,''),
		       coalesce(x.client_address,''), coalesce(x.request_id,''),
		       coalesce(x.trace_id,''), coalesce(x.observer_id,'')
		  from events_core c
		  left join events_context x
		    on x.profile = c.profile and x.id = c.id and x.recorded_at = c.recorded_at
		 where c.profile = ` + b.arg(q.Profile))

	// The grant. It is one more term rather than a layer above, so that there
	// is no path to a row outside it: a query that forgot to apply it would
	// have to forget to name a profile too.
	if len(q.Tenants) > 0 {
		b.sql.WriteString(" and c.tenant_id = any(" + b.arg(q.Tenants) + ")")
	}
	if err := writeFilter(b, q.Filter); err != nil {
		return index.Page{}, err
	}
	if q.After != nil {
		if err := writeBoundary(b, order, q.After, q.Backwards); err != nil {
			return index.Page{}, err
		}
	}

	b.sql.WriteString(" order by ")
	b.sql.WriteString(orderSQL(order, q.Backwards))
	// One more than asked for, so that "is there another page" is answered
	// without counting what is behind it.
	b.sql.WriteString(" limit " + b.arg(limit+1))

	rows, err := i.DB.Query(ctx, b.sql.String(), b.args...)
	if err != nil {
		return index.Page{}, fmt.Errorf("postgres: search: %w", err)
	}
	defer rows.Close()

	page := index.Page{}
	for rows.Next() {
		var r index.Row
		var seq int64
		if err := rows.Scan(
			&r.ID, &r.TenantID, &r.OccurredAt, &r.RecordedAt, &seq, &r.Source,
			&r.Action, &r.Operation, &r.Outcome, &r.TargetTypes, &r.TargetIDs,
			&r.ObjectKey, &r.Line,
			&r.ActorKind, &r.ActorID, &r.SubjectKind, &r.SubjectID,
			&r.ClientAddress, &r.RequestID, &r.TraceID, &r.ObserverID,
		); err != nil {
			return index.Page{}, fmt.Errorf("postgres: search: %w", err)
		}
		r.Sequence = uint64(seq)
		page.Rows = append(page.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return index.Page{}, fmt.Errorf("postgres: search: %w", err)
	}

	if len(page.Rows) > limit {
		page.More = true
		page.Rows = page.Rows[:limit]
	}
	// Reading backwards returns rows in reverse; a page is always given in its
	// forward order whichever way it was reached.
	if q.Backwards {
		for a, z := 0, len(page.Rows)-1; a < z; a, z = a+1, z-1 {
			page.Rows[a], page.Rows[z] = page.Rows[z], page.Rows[a]
		}
	}
	if n := len(page.Rows); n > 0 {
		page.Next = boundaryOf(page.Rows[n-1], order)
		page.Prev = boundaryOf(page.Rows[0], order)
	} else if q.After != nil {
		// An empty page keeps the boundary it was asked from, so that a tail
		// polling a quiet profile does not lose its place.
		page.Next = q.After
	}
	return page, nil
}

// sortOrder is the ordering, always ending in the identifier.
//
// Without that last term an ordering is not total: two rows with the same
// occurred time would have no defined order between them, and a keyset boundary
// on the tie would either repeat one or skip the other.
func sortOrder(sort []index.SortBy) []index.SortBy {
	out := make([]index.SortBy, 0, len(sort)+1)
	seen := map[string]bool{}
	for _, s := range sort {
		if s.Field == "" || seen[s.Field] {
			continue
		}
		seen[s.Field] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		out = append(out, index.SortBy{Field: index.SortOccurredAt, Descending: true})
	}
	if !seen[index.SortID] {
		out = append(out, index.SortBy{Field: index.SortID})
	}
	return out
}

func column(field string) (string, error) {
	switch field {
	case index.SortOccurredAt:
		return "c.occurred_at", nil
	case index.SortRecordedAt:
		return "c.recorded_at", nil
	case index.SortID:
		return "c.id", nil
	case index.SortAction:
		return "c.action", nil
	case index.SortTenantID:
		return "c.tenant_id", nil
	case index.SortSource:
		return "c.source", nil
	default:
		return "", fmt.Errorf("postgres: cannot sort by %q", field)
	}
}

func orderSQL(order []index.SortBy, backwards bool) string {
	parts := make([]string, 0, len(order))
	for _, s := range order {
		col, err := column(s.Field)
		if err != nil {
			continue
		}
		descending := s.Descending
		if backwards {
			descending = !descending
		}
		if descending {
			parts = append(parts, col+" desc")
		} else {
			parts = append(parts, col+" asc")
		}
	}
	return strings.Join(parts, ", ")
}

// writeBoundary turns a keyset boundary into the row comparison that resumes
// from it: the lexicographic "after this tuple" written out, because the sort
// terms may run in different directions and a row constructor cannot.
func writeBoundary(b *builder, order []index.SortBy, at *index.Boundary, backwards bool) error {
	if len(at.Values) != len(order)-1 {
		return fmt.Errorf(
			"postgres: the cursor carries %d sort values and this query has %d; "+
				"a cursor belongs to the query it came from",
			len(at.Values), len(order)-1)
	}
	var clauses []string
	for i := range order {
		var equals []string
		for j := 0; j < i; j++ {
			col, err := column(order[j].Field)
			if err != nil {
				return err
			}
			equals = append(equals, col+" = "+b.arg(at.Values[j]))
		}
		col, err := column(order[i].Field)
		if err != nil {
			return err
		}
		descending := order[i].Descending
		if backwards {
			descending = !descending
		}
		comparison := ">"
		if descending {
			comparison = "<"
		}
		value := at.ID
		if i < len(at.Values) {
			value = at.Values[i]
		}
		clauses = append(clauses, "("+strings.Join(append(equals, col+" "+comparison+" "+b.arg(value)), " and ")+")")
	}
	b.sql.WriteString(" and (" + strings.Join(clauses, " or ") + ")")
	return nil
}

func boundaryOf(r index.Row, order []index.SortBy) *index.Boundary {
	out := &index.Boundary{ID: r.ID, RecordedAt: r.RecordedAt, Sequence: r.Sequence}
	for _, s := range order[:len(order)-1] {
		switch s.Field {
		case index.SortOccurredAt:
			out.Values = append(out.Values, r.OccurredAt.UTC().Format(time.RFC3339Nano))
		case index.SortRecordedAt:
			out.Values = append(out.Values, r.RecordedAt.UTC().Format(time.RFC3339Nano))
		case index.SortAction:
			out.Values = append(out.Values, r.Action)
		case index.SortTenantID:
			out.Values = append(out.Values, r.TenantID)
		case index.SortSource:
			out.Values = append(out.Values, r.Source)
		}
	}
	return out
}

// Facets counts values under the same filter.
//
// The counts table answers the common case — a whole profile, no filter, which
// is what a viewer's navigation asks on every page — without touching the
// events at all. A filtered facet cannot use it, because the table counts rows
// and not rows-matching-something, so that falls back to counting the events.
func (i *Index) Facets(
	ctx context.Context, q index.Query, fields []string, limit int,
) ([]index.Facet, error) {
	if i.reader {
		var out []index.Facet
		err := AsTenants(ctx, i.DB, PinOf(q.Tenants), func(pinned *Index) error {
			var err error
			out, err = pinned.Facets(ctx, q, fields, limit)
			return err
		})
		return out, err
	}
	if q.Profile == "" {
		return nil, errors.New("postgres: facets name one profile")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	out := make([]index.Facet, 0, len(fields))
	for _, field := range fields {
		var values []index.FacetValue
		var err error
		if len(q.Filter) == 0 {
			values, err = i.countedFacet(ctx, q, field, limit)
		} else {
			values, err = i.scannedFacet(ctx, q, field, limit)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, index.Facet{Field: field, Values: values})
	}
	return out, nil
}

// countedFacet reads the counts the writer maintained.
func (i *Index) countedFacet(
	ctx context.Context, q index.Query, field string, limit int,
) ([]index.FacetValue, error) {
	b := &builder{}
	b.sql.WriteString(`
		select facet_value, sum(total)::bigint
		  from facet_counts
		 where profile = ` + b.arg(q.Profile) + ` and field = ` + b.arg(field))
	if len(q.Tenants) > 0 {
		b.sql.WriteString(" and tenant_id = any(" + b.arg(q.Tenants) + ")")
	}
	b.sql.WriteString(" group by facet_value having sum(total) > 0" +
		" order by sum(total) desc, facet_value asc limit " + b.arg(limit))
	return i.facetRows(ctx, b)
}

// scannedFacet counts the events themselves, for a facet under a filter.
func (i *Index) scannedFacet(
	ctx context.Context, q index.Query, field string, limit int,
) ([]index.FacetValue, error) {
	column, err := facetColumn(field)
	if err != nil {
		return nil, err
	}
	// A record naming one target type twice counts once, as the counts table
	// has it: the question is how many records touched it, not how many rows
	// the record had.
	if field == index.FieldTargetType {
		column = "t.value"
	}
	b := &builder{}
	b.sql.WriteString(`
		select ` + column + `, count(distinct c.id)::bigint
		  from events_core c
		  left join events_context x
		    on x.profile = c.profile and x.id = c.id and x.recorded_at = c.recorded_at`)
	if field == index.FieldTargetType {
		b.sql.WriteString(" cross join lateral unnest(c.target_types) as t(value)")
	}
	b.sql.WriteString(" where c.profile = " + b.arg(q.Profile))
	if len(q.Tenants) > 0 {
		b.sql.WriteString(" and c.tenant_id = any(" + b.arg(q.Tenants) + ")")
	}
	if err := writeFilter(b, q.Filter); err != nil {
		return nil, err
	}
	b.sql.WriteString(" group by 1 order by 2 desc, 1 asc limit " + b.arg(limit))
	return i.facetRows(ctx, b)
}

func (i *Index) facetRows(ctx context.Context, b *builder) ([]index.FacetValue, error) {
	rows, err := i.DB.Query(ctx, b.sql.String(), b.args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: facets: %w", err)
	}
	defer rows.Close()

	var out []index.FacetValue
	for rows.Next() {
		var v index.FacetValue
		if err := rows.Scan(&v.Value, &v.Count); err != nil {
			return nil, fmt.Errorf("postgres: facets: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: facets: %w", err)
	}
	return out, nil
}

// facetColumn maps a facet's name to its column. The names are the ones the
// writer counts under, so a viewer asks the same question of either path.
func facetColumn(field string) (string, error) {
	switch field {
	case index.FieldAction:
		return "c.action", nil
	case index.FieldOperation:
		return "c.operation", nil
	case index.FieldOutcome:
		return "c.outcome", nil
	case index.FieldSource:
		return "c.source", nil
	case index.FieldTargetType:
		return "c.target_types", nil
	case index.FieldActorKind:
		return "x.actor_kind", nil
	default:
		return "", fmt.Errorf(
			"postgres: %q is not a facet this searcher counts under a filter", field)
	}
}

// Get returns one row and where it came from.
//
// The provenance is the point: an answer a reader can check against the copy
// the digest chain accounts for is worth more than one they have to believe.
func (i *Index) Get(ctx context.Context, profile, id string) (index.Row, index.Provenance, error) {
	page, err := i.Search(ctx, index.Query{
		Profile: profile,
		Filter:  []index.Conjunction{{ID: []index.Predicate{{Op: index.Equal, Value: id}}}},
		Limit:   1,
	})
	if err != nil {
		return index.Row{}, index.Provenance{}, err
	}
	if len(page.Rows) == 0 {
		return index.Row{}, index.Provenance{}, fmt.Errorf("postgres: no record %s in profile %s", id, profile)
	}
	r := page.Rows[0]
	return r, index.Provenance{ObjectKey: r.ObjectKey, Line: r.Line}, nil
}
