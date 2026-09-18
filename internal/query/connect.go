package query

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/audit/auth"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/gen/audit/v1/auditv1connect"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/record"
	jsonwire "github.com/truvity/audit/wire"
)

// Handler serves the query service over Connect.
type Handler struct {
	auditv1connect.UnimplementedQueryServiceHandler
	Service *Service
	// Authenticator establishes who is asking. There is no default: a handler
	// that answered without one would answer anybody.
	Authenticator auth.Authenticator
}

// NewHandler returns the path and handler to mount.
func NewHandler(s *Service, a auth.Authenticator, opts ...connect.HandlerOption) (string, http.Handler) {
	return auditv1connect.NewQueryServiceHandler(&Handler{Service: s, Authenticator: a}, append(jsonwire.HandlerOptions(), opts...)...)
}

// Search implements the service.
func (h *Handler) Search(
	ctx context.Context, req *connect.Request[auditv1.SearchRequest],
) (*connect.Response[auditv1.SearchResponse], error) {
	who, err := h.who(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	page, grant, err := h.Service.Search(ctx, who, req.Msg)
	if err != nil {
		return nil, wire(err)
	}
	cursors, err := h.Service.Cursors(req.Msg, grant, page)
	if err != nil {
		return nil, wire(err)
	}

	items := make([]*auditv1.Record, 0, len(page.Rows))
	for _, r := range page.Rows {
		items = append(items, asRecord(r))
	}
	// The normalised query goes back with the page, so that a caller can see
	// what was actually asked — the grant may have narrowed it — and so that a
	// cursor has something to be checked against.
	echo := &auditv1.SearchRequest{
		Profile: req.Msg.GetProfile(), Filter: req.Msg.GetFilter(),
		Sort: req.Msg.GetSort(), Limit: req.Msg.GetLimit(),
	}
	return connect.NewResponse(&auditv1.SearchResponse{
		Items: items, Query: echo, Cursors: cursors,
	}), nil
}

// Access implements the service.
func (h *Handler) Access(
	ctx context.Context, req *connect.Request[auditv1.AccessRequest],
) (*connect.Response[auditv1.AccessResponse], error) {
	who, err := h.who(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	profiles, err := h.Service.Access(ctx, who)
	if err != nil {
		return nil, wire(err)
	}
	out := &auditv1.AccessResponse{}
	for _, p := range profiles {
		access := &auditv1.ProfileAccess{
			Profile:    p.Profile,
			AllTenants: p.Grant.AllTenants,
			Tenants:    p.Grant.Tenants,
		}
		for _, op := range p.Operations {
			access.Operations = append(access.Operations, string(op))
		}
		if !p.Grant.From.IsZero() {
			access.From = timestamppb.New(p.Grant.From)
		}
		if !p.Grant.Until.IsZero() {
			access.Until = timestamppb.New(p.Grant.Until)
		}
		out.Profiles = append(out.Profiles, access)
	}
	return connect.NewResponse(out), nil
}

// Facets implements the service.
func (h *Handler) Facets(
	ctx context.Context, req *connect.Request[auditv1.FacetsRequest],
) (*connect.Response[auditv1.FacetsResponse], error) {
	who, err := h.who(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	facets, _, err := h.Service.Facets(ctx, who, req.Msg)
	if err != nil {
		return nil, wire(err)
	}
	out := make([]*auditv1.Facet, 0, len(facets))
	for _, f := range facets {
		values := make([]*auditv1.FacetValue, 0, len(f.Values))
		for _, v := range f.Values {
			values = append(values, &auditv1.FacetValue{Value: v.Value, Count: v.Count})
		}
		out = append(out, &auditv1.Facet{Field: f.Field, Values: values})
	}
	return connect.NewResponse(&auditv1.FacetsResponse{Facets: out}), nil
}

// Get implements the service.
func (h *Handler) Get(
	ctx context.Context, req *connect.Request[auditv1.GetRequest],
) (*connect.Response[auditv1.GetResponse], error) {
	who, err := h.who(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	row, where, _, err := h.Service.Get(ctx, who, req.Msg)
	if err != nil {
		return nil, wire(err)
	}
	provenance := &auditv1.Provenance{
		ObjectKey: where.ObjectKey, Line: int64(where.Line), DigestId: where.Digest,
	}
	if !where.VerifiedAt.IsZero() {
		provenance.VerifiedAt = timestamppb.New(where.VerifiedAt)
	}
	return connect.NewResponse(&auditv1.GetResponse{Record: asRecord(row), Provenance: provenance}), nil
}

func (h *Handler) who(ctx context.Context, header http.Header) (auth.Principal, error) {
	if h.Authenticator == nil {
		return auth.Principal{}, connect.NewError(connect.CodeInternal,
			errors.New("query: no authenticator is configured, and this would otherwise answer anybody"))
	}
	p, err := h.Authenticator.Principal(ctx, &http.Request{Header: header})
	if err != nil {
		return auth.Principal{}, connect.NewError(connect.CodeUnauthenticated, err)
	}
	return p, nil
}

// Resolve implements the service.
func (h *Handler) Resolve(
	ctx context.Context, req *connect.Request[auditv1.ResolveRequest],
) (*connect.Response[auditv1.ResolveResponse], error) {
	who, err := h.who(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	id, err := h.Service.Resolve(ctx, who, req.Msg)
	if err != nil {
		return nil, wire(err)
	}
	return connect.NewResponse(&auditv1.ResolveResponse{Identifier: id}), nil
}

// wire maps an error to a code a client can act on.
//
// The direction matters in both senses. A denial or a bad cursor is the
// caller's to fix and must not read as a server fault, or a client retries it
// forever. And a searcher that is down is not the caller's fault: calling it
// invalid_argument tells a well-behaved client never to try again, which turns
// a database restart into an outage that outlives it.
//
// So the default is unavailable, not invalid_argument. A malformed request is
// recognised by having come from Compile, and everything unrecognised is
// treated as the service's problem rather than blamed on whoever asked.
func wire(err error) error {
	switch {
	case errors.Is(err, auth.ErrDenied):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, ErrCursorMismatch), errors.Is(err, ErrMalformed):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, ErrTooMuch):
		return connect.NewError(connect.CodeResourceExhausted, err)
	case errors.Is(err, ErrNotOffered):
		// Not configured here is not the caller's mistake and not a fault to
		// retry: it is a thing this deployment does not do.
		return connect.NewError(connect.CodeUnimplemented, err)
	case errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrErased):
		// Not a fault and not the caller's mistake: an erasure did what it is
		// for. Retrying will never succeed, which is what this code says.
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeUnavailable, err)
	}
}

// asRecord renders what the index holds.
//
// It is the searchable projection of a record, not the record: the index keeps
// the columns a query can name, and the rest of what was written stays in the
// archive. A caller who needs the whole thing asks Get, which reads the line
// the provenance points at. Returning a partial record from a search and a
// whole one from a get is the honest shape — the alternative is reading an
// object per row of every page.
func asRecord(r index.Row) *auditv1.Record {
	out := &auditv1.Record{
		Id: r.ID, TenantId: r.TenantID, Source: r.Source, Action: r.Action,
		Operation: record.ParseOperation(r.Operation),
		Outcome:   &auditv1.Outcome{Result: record.ParseResult(r.Outcome)},
		Actor:     &auditv1.Actor{Kind: r.ActorKind, Id: r.ActorID},
		Subject:   &auditv1.Party{Kind: r.SubjectKind, Id: r.SubjectID},
		Observer:  &auditv1.Observer{Id: r.ObserverID},
		Context:   &auditv1.Context{RequestId: r.RequestID, TraceId: r.TraceID},
	}
	if !r.OccurredAt.IsZero() {
		out.OccurredAt = timestamppb.New(r.OccurredAt)
	}
	if !r.RecordedAt.IsZero() {
		out.RecordedAt = timestamppb.New(r.RecordedAt)
	}
	if r.ClientAddress != "" {
		out.Context.ClientAddresses = []string{r.ClientAddress}
	}
	for i, kind := range r.TargetTypes {
		t := &auditv1.Target{Type: kind}
		if i < len(r.TargetIDs) {
			t.Id = r.TargetIDs[i]
		}
		out.Targets = append(out.Targets, t)
	}
	return out
}

// Export implements the service.
func (h *Handler) Export(
	ctx context.Context, req *connect.Request[auditv1.ExportRequest],
) (*connect.Response[auditv1.ExportResponse], error) {
	who, err := h.who(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	job, err := h.Service.Export(ctx, who, req.Msg)
	if err != nil {
		return nil, wire(err)
	}
	return connect.NewResponse(&auditv1.ExportResponse{
		JobId: job.ID, State: job.State(),
	}), nil
}

// GetExport implements the service.
func (h *Handler) GetExport(
	ctx context.Context, req *connect.Request[auditv1.GetExportRequest],
) (*connect.Response[auditv1.GetExportResponse], error) {
	who, err := h.who(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	job, url, err := h.Service.GetExport(ctx, who, req.Msg.GetJobId())
	if err != nil {
		return nil, wire(err)
	}
	out := &auditv1.GetExportResponse{
		State: job.State(), Url: url,
		Records: int64(job.Records), Error: job.Failed,
	}
	if !job.ExpiresAt.IsZero() {
		out.ExpiresAt = timestamppb.New(job.ExpiresAt)
	}
	return connect.NewResponse(out), nil
}
