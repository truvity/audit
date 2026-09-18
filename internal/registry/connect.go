package registry

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/gen/audit/v1/auditv1connect"
	"github.com/truvity/audit/wire"
)

// Handler serves the registry over Connect.
type Handler struct {
	auditv1connect.UnimplementedRegistryServiceHandler
	Registry *Registry
}

// NewHandler returns the path and handler to mount.
func NewHandler(r *Registry, opts ...connect.HandlerOption) (string, http.Handler) {
	return auditv1connect.NewRegistryServiceHandler(&Handler{Registry: r}, append(wire.HandlerOptions(), opts...)...)
}

// RegisterCatalogue implements the service.
//
// Validation problems come back as a response rather than an error: they are
// the application's to fix and it wants the whole list, and a transport error
// would make a client retry something that will never succeed.
func (h *Handler) RegisterCatalogue(
	ctx context.Context, req *connect.Request[auditv1.RegisterCatalogueRequest],
) (*connect.Response[auditv1.RegisterCatalogueResponse], error) {
	c := req.Msg.GetCatalogue()
	if c == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no catalogue"))
	}
	problems, err := h.Registry.Register(ctx, Entry{
		Source: c.GetSource(), Version: c.GetVersion(),
		Document: c.GetDocument(), Schemas: c.GetSchemas(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&auditv1.RegisterCatalogueResponse{Problems: problems}), nil
}

// GetCatalogue implements the service.
func (h *Handler) GetCatalogue(
	ctx context.Context, req *connect.Request[auditv1.GetCatalogueRequest],
) (*connect.Response[auditv1.GetCatalogueResponse], error) {
	e, err := h.Registry.Entry(ctx, req.Msg.GetSource(), req.Msg.GetVersion())
	if errors.Is(err, ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&auditv1.GetCatalogueResponse{Catalogue: asProto(e)}), nil
}

// ListCatalogues implements the service.
func (h *Handler) ListCatalogues(
	ctx context.Context, _ *connect.Request[auditv1.ListCataloguesRequest],
) (*connect.Response[auditv1.ListCataloguesResponse], error) {
	entries, err := h.Registry.List(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*auditv1.Catalogue, 0, len(entries))
	for _, e := range entries {
		out = append(out, asProto(e))
	}
	return connect.NewResponse(&auditv1.ListCataloguesResponse{Catalogues: out}), nil
}

func asProto(e Entry) *auditv1.Catalogue {
	return &auditv1.Catalogue{
		Source: e.Source, Version: e.Version,
		Document: e.Document, Schemas: e.Schemas,
		RegisteredAt: timestamppb.New(e.RegisteredAt),
	}
}
