// Package query is the read service: it turns a request into a closed query,
// narrows it to what the caller was granted, asks a searcher, and records that
// the read happened.
//
// It is internal because the contract is the API, not this Go package. What a
// third party implements is the Searcher below it and the Authenticator and
// Authorizer above it, both of which are public.
package query

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/index"
)

// The kinds of failure a caller can tell apart. They exist so that the wire
// mapping is a fact about the error rather than a guess from its text.
var (
	// ErrMalformed is a request this service cannot read. The caller fixes it.
	ErrMalformed = errors.New("query: malformed request")
	// ErrTooMuch is a request that asks for more than a limit allows.
	ErrTooMuch = errors.New("query: more than this deployment allows")
	// ErrNotOffered is something this deployment does not do.
	ErrNotOffered = errors.New("query: not offered by this deployment")
	// ErrNotFound is a record or a job that is not there — or that the caller
	// may not know is.
	ErrNotFound = errors.New("query: not found")
)

// maxConjunctions bounds the OR before a searcher is even asked, so that the
// refusal is the service's and does not depend on which searcher is configured.
const maxConjunctions = 4

// maxSort and maxIn are the other two bounds the reference publishes. They are
// enforced here rather than left to a searcher, so that the answer to "is this
// too much" does not depend on which one is configured.
const (
	maxSort = 4
	maxIn   = 100
)

// Compile turns a request into a closed query.
//
// Every predicate is named by the caller from a fixed set; nothing here parses
// an expression. That is what makes a grant expressible as one more term rather
// than as a rewrite of something arbitrary.
func Compile(req *auditv1.SearchRequest) (index.Query, error) {
	if req.GetProfile() == "" {
		return index.Query{}, fmt.Errorf("%w: name a profile", ErrMalformed)
	}
	if len(req.GetFilter()) > maxConjunctions {
		return index.Query{}, fmt.Errorf(
			"%w: %d conjunctions, and %d is the most: an unbounded OR is unbounded work "+
				"on a table that only grows", ErrTooMuch, len(req.GetFilter()), maxConjunctions)
	}
	if len(req.GetSort()) > maxSort {
		return index.Query{}, fmt.Errorf(
			"%w: %d sort terms, and %d is the most", ErrTooMuch, len(req.GetSort()), maxSort)
	}

	q := index.Query{Profile: req.GetProfile(), Limit: int(req.GetLimit())}
	for _, f := range req.GetFilter() {
		c, err := conjunction(f)
		if err != nil {
			return index.Query{}, err
		}
		q.Filter = append(q.Filter, c)
	}
	for _, s := range req.GetSort() {
		field, err := sortField(s.GetField())
		if err != nil {
			return index.Query{}, err
		}
		q.Sort = append(q.Sort, index.SortBy{
			Field: field, Descending: s.GetOrder() == auditv1.Sort_ORDER_DESC,
		})
	}
	return q, nil
}

func conjunction(f *auditv1.Filter) (index.Conjunction, error) {
	c := index.Conjunction{}
	pairs := []struct {
		into *[]index.Predicate
		from *auditv1.StringPredicate
	}{
		{&c.ID, f.GetId()},
		{&c.Source, f.GetSource()},
		{&c.Action, f.GetAction()},
		{&c.Operation, f.GetOperation()},
		{&c.Outcome, f.GetOutcome()},
		{&c.TenantID, f.GetTenantId()},
		{&c.ActorKind, f.GetActorKind()},
		{&c.ActorID, f.GetActorId()},
		{&c.SubjectKind, f.GetSubjectKind()},
		{&c.SubjectID, f.GetSubjectId()},
		{&c.RequestID, f.GetRequestId()},
		{&c.TraceID, f.GetTraceId()},
		{&c.ClientAddress, f.GetClientAddress()},
		{&c.ObserverID, f.GetObserverId()},
	}
	for _, p := range pairs {
		if p.from == nil {
			continue
		}
		got, err := predicate(p.from)
		if err != nil {
			return c, err
		}
		*p.into = append(*p.into, got)
	}

	if err := wholeIDs(c.ID); err != nil {
		return c, err
	}

	if w := f.GetOccurredAt(); w != nil {
		c.OccurredAt = append(c.OccurredAt, window(w))
	}
	if w := f.GetRecordedAt(); w != nil {
		c.RecordedAt = append(c.RecordedAt, window(w))
	}
	if t := f.GetTargets(); t != nil {
		if list := t.GetIn(); list != nil {
			for _, ref := range list.GetValues() {
				if ref.GetType() != "" {
					c.TargetType = append(c.TargetType, index.Predicate{Op: index.Equal, Value: ref.GetType()})
				}
				if ref.GetId() != "" {
					c.TargetID = append(c.TargetID, index.Predicate{Op: index.Equal, Value: ref.GetId()})
				}
			}
		}
	}
	for _, p := range f.GetData() {
		got, err := path(p)
		if err != nil {
			return c, err
		}
		c.Data = append(c.Data, got)
	}
	return c, nil
}

func predicate(p *auditv1.StringPredicate) (index.Predicate, error) {
	switch {
	case p.GetEqual() != "":
		return index.Predicate{Op: index.Equal, Value: p.GetEqual()}, nil
	case p.GetNotEqual() != "":
		return index.Predicate{Op: index.NotEqual, Value: p.GetNotEqual()}, nil
	case p.GetIn() != nil:
		return listPredicate(index.In, p.GetIn().GetValues())
	case p.GetNotIn() != nil:
		return listPredicate(index.NotIn, p.GetNotIn().GetValues())
	case p.GetPrefix() != "":
		return index.Predicate{Op: index.Prefix, Value: p.GetPrefix()}, nil
	default:
		return index.Predicate{}, fmt.Errorf("%w: a predicate with no operator", ErrMalformed)
	}
}

// listPredicate bounds an `in`. A list nobody bounded is a query nobody
// bounded: the values all reach the index, and a thousand of them is a
// thousand comparisons per row.
func listPredicate(op index.Op, values []string) (index.Predicate, error) {
	if len(values) > maxIn {
		return index.Predicate{}, fmt.Errorf(
			"%w: %d values in an `%s`, and %d is the most", ErrTooMuch, len(values), op, maxIn)
	}
	if len(values) == 0 {
		return index.Predicate{}, fmt.Errorf("%w: an empty `%s` matches nothing; leave it out", ErrMalformed, op)
	}
	return index.Predicate{Op: op, Values: values}, nil
}

func path(p *auditv1.PathPredicate) (index.PathPredicate, error) {
	out := index.PathPredicate{Path: p.GetPath(), Kind: index.Text, Op: index.Equal}
	if out.Path == "" {
		return out, fmt.Errorf("%w: a data predicate with no path", ErrMalformed)
	}
	switch {
	case p.GetString_() != nil:
		got, err := predicate(p.GetString_())
		if err != nil {
			return out, err
		}
		out.Op, out.Text, out.Values = got.Op, got.Value, got.Values
	case p.GetInteger() != nil:
		out.Kind, out.Int = index.Int, p.GetInteger().GetEqual()
	case p.GetTime() != nil:
		out.Kind = index.Time
		// A time predicate on a data property is a range like any other, and
		// the index stores the value, so an exact moment is a range of one.
		w := window(p.GetTime())
		out.At = w.From
	default:
		return out, fmt.Errorf("%w: a data predicate on %s with no value", ErrMalformed, out.Path)
	}
	return out, nil
}

// window turns a time predicate into the half-open range the index takes.
//
// Every operator becomes a range, because that is the only shape an index can
// answer without reading rows it will discard. An exclusive bound and an
// inclusive one differ by the smallest time the record format keeps, which is a
// nanosecond: the alternative is two comparison shapes everywhere downstream
// for a distinction nothing in an audit trail turns on.
func window(p *auditv1.TimePredicate) index.TimePredicate {
	var out index.TimePredicate
	switch {
	case p.GetBetween() != nil:
		if f := p.GetBetween().GetFrom(); f != nil {
			out.From = f.AsTime()
		}
		if t := p.GetBetween().GetTo(); t != nil {
			out.To = t.AsTime()
		}
	case p.GetGreaterThan() != nil:
		out.From = p.GetGreaterThan().AsTime().Add(time.Nanosecond)
	case p.GetGreaterThanOrEqual() != nil:
		out.From = p.GetGreaterThanOrEqual().AsTime()
	case p.GetLessThan() != nil:
		out.To = p.GetLessThan().AsTime()
	case p.GetLessThanOrEqual() != nil:
		out.To = p.GetLessThanOrEqual().AsTime().Add(time.Nanosecond)
	}
	return out
}

func sortField(f auditv1.Sort_Field) (string, error) {
	switch f {
	case auditv1.Sort_FIELD_OCCURRED_AT:
		return index.SortOccurredAt, nil
	case auditv1.Sort_FIELD_RECORDED_AT:
		return index.SortRecordedAt, nil
	case auditv1.Sort_FIELD_ID:
		return index.SortID, nil
	case auditv1.Sort_FIELD_ACTION:
		return index.SortAction, nil
	case auditv1.Sort_FIELD_TENANT_ID:
		return index.SortTenantID, nil
	case auditv1.Sort_FIELD_SOURCE:
		return index.SortSource, nil
	default:
		return "", fmt.Errorf("%w: %s is not a field this service sorts by", ErrMalformed, f)
	}
}

// clamp keeps a caller's window inside the grant's.
//
// It narrows and never widens: a grant that allows the last ninety days and a
// request for the last year is answered with ninety days, not refused. The
// caller is told what they got through the normalised query in the response.
func clamp(q index.Query, from, until time.Time) index.Query {
	if from.IsZero() && until.IsZero() {
		return q
	}
	if len(q.Filter) == 0 {
		q.Filter = []index.Conjunction{{}}
	}
	for i := range q.Filter {
		q.Filter[i].OccurredAt = append(q.Filter[i].OccurredAt, index.TimePredicate{From: from, To: until})
	}
	return q
}

// wholeIDs holds id predicates to what an identifier is: a UUID, whole. A
// record's id is always one (the record validator refuses anything else), so
// a value that is not can match nothing — and a searcher that stores ids as
// UUIDs would fail on it rather than answer, which a caller would read as an
// outage and retry. A prefix is refused for the same reason: an identifier is
// looked up, not ranged over.
func wholeIDs(predicates []index.Predicate) error {
	for _, p := range predicates {
		values := p.Values
		switch p.Op {
		case index.Prefix:
			return fmt.Errorf("%w: id takes whole identifiers, not a prefix", ErrMalformed)
		case index.Equal, index.NotEqual:
			values = []string{p.Value}
		}
		for _, v := range values {
			if _, err := uuid.Parse(v); err != nil {
				return fmt.Errorf("%w: id %q is not an identifier; record ids are UUIDs", ErrMalformed, v)
			}
		}
	}
	return nil
}
