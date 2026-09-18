// Package query is the read service: it turns a request into a closed query,
// narrows it to what the caller was granted, asks a searcher, and records that
// the read happened.
//
// It is internal because the contract is the API, not this Go package. What a
// third party implements is the Searcher below it and the Authenticator and
// Authorizer above it, both of which are public.
package query

import (
	"fmt"
	"time"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/index"
)

// maxConjunctions bounds the OR before a searcher is even asked, so that the
// refusal is the service's and does not depend on which searcher is configured.
const maxConjunctions = 4

// Compile turns a request into a closed query.
//
// Every predicate is named by the caller from a fixed set; nothing here parses
// an expression. That is what makes a grant expressible as one more term rather
// than as a rewrite of something arbitrary.
func Compile(req *auditv1.SearchRequest) (index.Query, error) {
	if req.GetProfile() == "" {
		return index.Query{}, fmt.Errorf("query: name a profile")
	}
	if len(req.GetFilter()) > maxConjunctions {
		return index.Query{}, fmt.Errorf(
			"query: %d conjunctions, and %d is the most: an unbounded OR is unbounded work "+
				"on a table that only grows", len(req.GetFilter()), maxConjunctions)
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
		return index.Predicate{Op: index.In, Values: p.GetIn().GetValues()}, nil
	case p.GetNotIn() != nil:
		return index.Predicate{Op: index.NotIn, Values: p.GetNotIn().GetValues()}, nil
	case p.GetPrefix() != "":
		return index.Predicate{Op: index.Prefix, Value: p.GetPrefix()}, nil
	default:
		return index.Predicate{}, fmt.Errorf("query: a predicate with no operator")
	}
}

func path(p *auditv1.PathPredicate) (index.PathPredicate, error) {
	out := index.PathPredicate{Path: p.GetPath(), Kind: index.Text, Op: index.Equal}
	if out.Path == "" {
		return out, fmt.Errorf("query: a data predicate with no path")
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
		return out, fmt.Errorf("query: a data predicate on %s with no value", out.Path)
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
		return "", fmt.Errorf("query: %s is not a field this service sorts by", f)
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
