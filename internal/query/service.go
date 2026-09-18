package query

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/emit"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// Service answers queries, within a grant, and records that it did.
//
// Reads of an audit trail are themselves auditable events: a trail that shows
// what everyone did except who looked at it is missing the half an investigation
// usually starts from. Every answer here produces a record naming the caller,
// what they asked and the rule that let them.
type Service struct {
	Searcher index.Searcher
	// Authorizer decides what the caller may see. There is no default: a
	// service that answered without one would answer everything.
	Authorizer auth.Authorizer
	// Sink and Catalogue are where reads are recorded. Without them the service
	// runs and the reading of the trail leaves no trace, which a deployment
	// should have to choose rather than fall into.
	Sink      sink.Sink
	Catalogue *catalogue.Catalogue
	// Exporter writes exports, when a deployment offers them. Without one the
	// export operation is refused: a grant may name it, and nothing here will
	// produce a copy of records that the deployment did not configure a place
	// for.
	Exporter *Exporter
	Version  string
	Instance string
	// OnUnrecorded is called when a read happened and the trail does not say
	// so. A deployment alerts on it: the reading of an audit trail going
	// unrecorded is not a degraded service, it is the service failing at one of
	// the two things it is for.
	OnUnrecorded func(action string, err error)

	emitter *emit.Emitter
}

// New checks a service's parts and prepares its own emitter.
func New(s *Service) (*Service, error) {
	switch {
	case s.Searcher == nil:
		return nil, errors.New("query: a searcher is required")
	case s.Authorizer == nil:
		return nil, errors.New(
			"query: an authorizer is required: a service that answered without one would answer everything")
	}
	if s.Sink != nil && s.Catalogue != nil {
		emitter, err := emit.New(emit.Options{
			Source: s.Catalogue.Source, Catalogue: s.Catalogue, Sink: s.Sink,
			// The service's account of its own reads. Best-effort whatever the
			// catalogue declares: a read that blocked on recording itself
			// could not report that it had failed to.
			SelfReporting: true,
			Version:       s.Version, Instance: s.instance(),
			Hooks: emit.Hooks{
				OnDropped: func(r *record.Record, reason string) {
					s.unrecorded(r.GetAction(), errors.New(reason))
				},
				OnRefused: func(r *record.Record, err error) {
					s.unrecorded(r.GetAction(), err)
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("query: %w", err)
		}
		s.emitter = emitter
	}
	return s, nil
}

// Close drains what is pending.
func (s *Service) Close() error {
	if s.emitter != nil {
		return s.emitter.Close()
	}
	return nil
}

// Search answers a page, narrowed to the grant.
func (s *Service) Search(
	ctx context.Context, p auth.Principal, req *auditv1.SearchRequest,
) (index.Page, auth.Grant, error) {
	g, err := s.allow(ctx, p, req.GetProfile(), auth.Search)
	if err != nil {
		return index.Page{}, g, err
	}
	compiled, err := Compile(req)
	if err != nil {
		return index.Page{}, g, err
	}
	q := s.narrow(compiled, g)

	// The cursor is bound to this question, narrowing included, so one issued
	// under a different grant or a different filter is refused rather than
	// resumed from a position in an ordering that no longer exists.
	mark, err := fingerprint(q)
	if err != nil {
		return index.Page{}, g, err
	}
	if q.After, q.Backwards, err = decodeCursor(req.GetCursor(), mark); err != nil {
		return index.Page{}, g, err
	}

	page, err := s.Searcher.Search(ctx, q)
	// The read is recorded whether or not it succeeded. An attempt to read the
	// trail is as much a fact about who was looking as a successful one, and a
	// refused attempt is the more interesting of the two.
	s.recordSearch(ctx, p, g, req.GetProfile(), len(page.Rows), err)
	return page, g, err
}

// Cursors renders a page's boundaries for the wire.
//
// `next` is present even on the last page, because a tail keeps polling it and
// a record written afterwards has to come back through it. There is no `last`:
// counting what is behind a query is the expense keyset paging exists to avoid.
func (s *Service) Cursors(
	req *auditv1.SearchRequest, g auth.Grant, page index.Page,
) (*auditv1.Cursors, error) {
	compiled, err := Compile(req)
	if err != nil {
		return nil, err
	}
	mark, err := fingerprint(s.narrow(compiled, g))
	if err != nil {
		return nil, err
	}
	out := &auditv1.Cursors{Self: req.GetCursor()}
	if out.Next, err = encodeCursor(page.Next, false, mark); err != nil {
		return nil, err
	}
	// `prev` reads the other way from the first row of this page. It is absent
	// on a page that was not reached by a cursor, because there is nothing
	// before the beginning.
	if req.GetCursor() != "" {
		if out.Prev, err = encodeCursor(page.Prev, true, mark); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Facets answers counts, narrowed to the grant.
func (s *Service) Facets(
	ctx context.Context, p auth.Principal, req *auditv1.FacetsRequest,
) ([]index.Facet, auth.Grant, error) {
	g, err := s.allow(ctx, p, req.GetProfile(), auth.Facets)
	if err != nil {
		return nil, g, err
	}
	compiled, err := Compile(&auditv1.SearchRequest{
		Profile: req.GetProfile(), Filter: req.GetFilter(),
	})
	if err != nil {
		return nil, g, err
	}
	q := s.narrow(compiled, g)

	facets, err := s.Searcher.Facets(ctx, q, req.GetFields(), int(req.GetLimitPerField()))
	s.record(ctx, "audit.facets", p, g, err, append([]*record.Target{
		{Type: "profile", Id: req.GetProfile()},
	}, tenantTargets(g)...))
	return facets, g, err
}

// Get answers one record, and refuses one the grant's tenants do not cover.
func (s *Service) Get(
	ctx context.Context, p auth.Principal, req *auditv1.GetRequest,
) (index.Row, index.Provenance, auth.Grant, error) {
	g, err := s.allow(ctx, p, req.GetProfile(), auth.Get)
	if err != nil {
		return index.Row{}, index.Provenance{}, g, err
	}

	row, where, err := s.Searcher.Get(ctx, req.GetProfile(), req.GetId())
	if err == nil && !granted(row.TenantID, g) {
		// Found, and not this caller's to see. It is reported as absent rather
		// than as forbidden: "no such record" and "a record you may not read"
		// are the same answer to someone who should not know it exists.
		err = fmt.Errorf("query: no record %s in profile %s", req.GetId(), req.GetProfile())
		row, where = index.Row{}, index.Provenance{}
	}
	// audit.get declares only the record it read.
	s.record(ctx, "audit.get", p, g, err, []*record.Target{
		{Type: "record", Id: req.GetId()},
	})
	return row, where, g, err
}

// allow authorizes the caller for one profile and operation.
//
// The grant comes back even on a refusal, because the refusal is recorded and
// the record names the rule that did not stretch far enough.
func (s *Service) allow(
	ctx context.Context, p auth.Principal, profile string, op auth.Operation,
) (auth.Grant, error) {
	g, err := s.Authorizer.Grant(ctx, p)
	if err != nil {
		return g, err
	}
	return g, g.Check(profile, op)
}

// narrow applies the grant to a compiled query.
//
// The tenant list becomes a term and the window becomes a predicate, so there
// is no path from a request to a row outside the grant: a query that lost the
// narrowing would have to have lost the query.
func (s *Service) narrow(q index.Query, g auth.Grant) index.Query {
	q.Tenants = g.TenantFilter()
	return clamp(q, g.From, g.Until)
}

func granted(tenant string, g auth.Grant) bool {
	if g.AllTenants {
		return true
	}
	for _, t := range g.Tenants {
		if t == tenant {
			return true
		}
	}
	return false
}

func (s *Service) recordSearch(
	ctx context.Context, p auth.Principal, g auth.Grant, profile string, rows int, failure error,
) {
	s.record(ctx, "audit.search", p, g, failure, append([]*record.Target{
		{Type: "profile", Id: profile},
	}, tenantTargets(g)...))
	_ = rows
}

// record puts one read in the trail.
//
// The actor is the caller as authenticated, and the outcome's reason carries
// the rule that granted it — a read nobody can trace to a rule is one nobody
// can review.
func (s *Service) record(
	ctx context.Context, action string, p auth.Principal, g auth.Grant,
	failure error, targets []*record.Target,
) {
	s.recordWithData(ctx, action, p, g, failure, targets, nil)
}

// recordWithData is the same, for an action whose catalogue entry declares a
// data slot.
func (s *Service) recordWithData(
	ctx context.Context, action string, p auth.Principal, g auth.Grant,
	failure error, targets []*record.Target, data *structpb.Struct,
) {
	if s.emitter == nil {
		return
	}
	r := &record.Record{
		Action:    action,
		Operation: auditv1.Operation_OPERATION_ACCESS,
		TenantId:  record.TenantPlatform,
		// "operator" rather than "person": the common catalogue declares the
		// kinds, and whoever reads an audit trail is acting in an internal
		// role. A record whose actor kind the catalogue does not declare is
		// refused, which is how this was found.
		// The record carries how the caller authenticated as well as who: a
		// subject from a gateway and the same subject from a bearer token are
		// different assurances, and "who read the audit log" is a poor answer
		// without the difference.
		Actor:   &record.Actor{Kind: "operator", Id: p.Subject, AuthMethod: p.Via},
		Targets: targets,
	}
	if failure != nil {
		r.Outcome = &record.Outcome{
			Result: auditv1.Outcome_RESULT_FAILURE,
			Reason: failure.Error(),
		}
	} else {
		r.Outcome = &record.Outcome{
			Result: auditv1.Outcome_RESULT_SUCCESS,
			Reason: g.Rule,
		}
	}
	r.Data = data
	if err := s.emitter.Record(ctx, r); err != nil {
		s.unrecorded(action, err)
	}
}

// structData builds an action's extension data.
func structData(from map[string]any) (*structpb.Struct, error) {
	return structpb.NewStruct(from)
}

func (s *Service) unrecorded(action string, err error) {
	if s.OnUnrecorded != nil {
		s.OnUnrecorded(action, err)
	}
}

// tenantTargets names the tenants a read was narrowed to, so that "who read
// this tenant's records" is a target lookup rather than a reconstruction from
// grants. An operator's grant over every tenant names none: the profile target
// and the rule already say so, and a list of every tenant would be a copy of
// the directory on every read.
//
// Each caller adds these itself rather than the recorder adding them to
// everything, because the catalogue says per action which targets it acts on
// and a record naming one it does not declare is refused.
func tenantTargets(g auth.Grant) []*record.Target {
	if g.AllTenants {
		return nil
	}
	out := make([]*record.Target, 0, len(g.Tenants))
	for _, t := range g.Tenants {
		out = append(out, &record.Target{Type: "tenant", Id: t})
	}
	return out
}

func (s *Service) instance() string {
	if s.Instance != "" {
		return s.Instance
	}
	return record.InstanceName()
}
