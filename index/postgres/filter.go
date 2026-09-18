package postgres

import (
	"fmt"
	"strings"

	"github.com/truvity/audit/index"
)

// writeFilter turns the OR of conjunctions into SQL.
//
// Every value a caller supplied becomes a parameter. Nothing a caller sent is
// ever spliced into the statement, and the column names come from a fixed table
// rather than from the request, so a field this searcher does not know is an
// error and never a fragment of SQL.
func writeFilter(b *builder, filter []index.Conjunction) error {
	if len(filter) == 0 {
		return nil
	}
	var ors []string
	for _, c := range filter {
		clauses, err := conjunction(b, c)
		if err != nil {
			return err
		}
		if len(clauses) == 0 {
			// An empty conjunction matches everything, so the whole OR does.
			return nil
		}
		ors = append(ors, "("+strings.Join(clauses, " and ")+")")
	}
	b.sql.WriteString(" and (" + strings.Join(ors, " or ") + ")")
	return nil
}

func conjunction(b *builder, c index.Conjunction) ([]string, error) {
	var out []string
	text := []struct {
		column string
		on     []index.Predicate
	}{
		{"c.id", c.ID},
		{"c.source", c.Source},
		{"c.action", c.Action},
		{"c.operation", c.Operation},
		{"c.outcome", c.Outcome},
		{"c.tenant_id", c.TenantID},
		{"x.actor_kind", c.ActorKind},
		{"x.actor_id", c.ActorID},
		{"x.subject_kind", c.SubjectKind},
		{"x.subject_id", c.SubjectID},
		{"x.request_id", c.RequestID},
		{"x.trace_id", c.TraceID},
		{"x.client_address", c.ClientAddress},
		{"x.observer_id", c.ObserverID},
	}
	for _, t := range text {
		for _, p := range t.on {
			clause, err := comparison(b, t.column, p)
			if err != nil {
				return nil, err
			}
			out = append(out, clause)
		}
	}

	for _, r := range c.OccurredAt {
		out = append(out, between(b, "c.occurred_at", r)...)
	}
	for _, r := range c.RecordedAt {
		out = append(out, between(b, "c.recorded_at", r)...)
	}

	// Targets are arrays on the row, so a predicate on them asks whether the
	// array contains the value rather than equals it.
	for _, p := range c.TargetType {
		clause, err := array(b, "c.target_types", p)
		if err != nil {
			return nil, err
		}
		out = append(out, clause)
	}
	for _, p := range c.TargetID {
		clause, err := array(b, "c.target_ids", p)
		if err != nil {
			return nil, err
		}
		out = append(out, clause)
	}

	for _, p := range c.Data {
		clause, err := data(b, p)
		if err != nil {
			return nil, err
		}
		out = append(out, clause)
	}
	return out, nil
}

func comparison(b *builder, column string, p index.Predicate) (string, error) {
	switch p.Op {
	case index.Equal:
		return column + " = " + b.arg(p.Value), nil
	case index.NotEqual:
		return column + " <> " + b.arg(p.Value), nil
	case index.In:
		return column + " = any(" + b.arg(p.Values) + ")", nil
	case index.NotIn:
		return "not (" + column + " = any(" + b.arg(p.Values) + "))", nil
	case index.Prefix:
		// A prefix is a range, not a pattern: it reads the same index a
		// sorted scan does, and it cannot be turned into a leading wildcard.
		return column + " >= " + b.arg(p.Value) + " and " + column + " < " + b.arg(upperBound(p.Value)), nil
	default:
		return "", fmt.Errorf("postgres: %q is not an operator this searcher has", p.Op)
	}
}

// upperBound is the first string after every string with this prefix.
func upperBound(prefix string) string {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xff {
			b[i]++
			return string(b[:i+1])
		}
	}
	// Every byte was 0xff, so nothing sorts after it; the range is open.
	return ""
}

func between(b *builder, column string, r index.TimePredicate) []string {
	var out []string
	if !r.From.IsZero() {
		out = append(out, column+" >= "+b.arg(r.From.UTC()))
	}
	if !r.To.IsZero() {
		// Exclusive, so that adjacent ranges neither overlap nor leave a gap.
		out = append(out, column+" < "+b.arg(r.To.UTC()))
	}
	return out
}

func array(b *builder, column string, p index.Predicate) (string, error) {
	switch p.Op {
	case index.Equal:
		return b.arg(p.Value) + " = any(" + column + ")", nil
	case index.NotEqual:
		return "not (" + b.arg(p.Value) + " = any(" + column + "))", nil
	case index.In:
		return column + " && " + b.arg(p.Values), nil
	case index.NotIn:
		return "not (" + column + " && " + b.arg(p.Values) + ")", nil
	default:
		return "", fmt.Errorf("postgres: %q cannot be asked of a target list", p.Op)
	}
}

// data is a predicate on an indexed extension property.
//
// It is an exists over events_data rather than a join, so that two predicates
// on two properties of the same record both hold rather than multiplying the
// row out.
func data(b *builder, p index.PathPredicate) (string, error) {
	var value string
	switch p.Kind {
	case index.Int, index.Bool:
		value = "d.value_int = " + b.arg(p.Int)
	case index.Time:
		value = "d.value_time = " + b.arg(p.At.UTC())
	default:
		clause, err := comparison(b, "d.value_text", index.Predicate{Op: p.Op, Value: p.Text, Values: p.Values})
		if err != nil {
			return "", err
		}
		value = clause
	}
	return "exists (select 1 from events_data d" +
		" where d.profile = c.profile and d.id = c.id and d.recorded_at = c.recorded_at" +
		" and d.path = " + b.arg(p.Path) + " and " + value + ")", nil
}
