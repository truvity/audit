// Package sinkserver is the receiving side of the sink: the SinkService handler
// the writer mounts, and the Receiver that checks a caller and a batch before
// handing it to a Sink.
//
// The client side, and the Sink interface both sides share, are in the SDK's
// sink package, which carries none of the server's dependencies.
package sinkserver

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/trace"

	"github.com/truvity/audit/internal/telemetry"
	auditv1 "github.com/truvity/audit/sdk/gen/audit/v1"
	"github.com/truvity/audit/sdk/gen/audit/v1/auditv1connect"
	"github.com/truvity/audit/sdk/sink"
	"github.com/truvity/audit/wire"
)

// Handler serves SinkService from a Sink, which is how the writer is reached
// by anything that cannot call it in process.
type Handler struct {
	auditv1connect.UnimplementedSinkServiceHandler
	sink sink.Sink
}

// NewHandler returns the path and handler to mount.
func NewHandler(s sink.Sink, opts ...connect.HandlerOption) (string, http.Handler) {
	return auditv1connect.NewSinkServiceHandler(&Handler{sink: s}, append(append(wire.HandlerOptions(), telemetry.ConnectOptions()...), opts...)...)
}

// Write implements the service.
func (h *Handler) Write(
	ctx context.Context, req *connect.Request[auditv1.WriteRequest],
) (*connect.Response[auditv1.WriteResponse], error) {
	in := &sink.Request{
		Records:  req.Msg.GetRecords(),
		Delivery: req.Msg.GetDelivery(),
	}
	ctx, done := sink.Observe(ctx, sink.TransportConnectServer, trace.SpanKindInternal, in)
	res, err := h.sink.Write(ctx, in)
	done(res, err)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(toProto(res)), nil
}

func toProto(r *sink.Result) *auditv1.WriteResponse {
	if r == nil {
		return &auditv1.WriteResponse{}
	}
	out := &auditv1.WriteResponse{Accepted: int32(r.Accepted), Durability: r.Durability}
	for _, x := range r.Rejected {
		out.Rejected = append(out.Rejected, &auditv1.Rejection{Id: x.ID, Reason: x.Reason})
	}
	return out
}
