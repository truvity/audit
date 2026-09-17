package sink

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/gen/audit/v1/auditv1connect"
)

// Client is a Sink that calls SinkService on another process: what an adapter
// outside the cluster uses, and what a queue consumer calls on the writer.
type Client struct {
	client auditv1connect.SinkServiceClient
}

// NewClient returns a Sink backed by the SinkService at baseURL.
func NewClient(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{client: auditv1connect.NewSinkServiceClient(httpClient, baseURL, opts...)}
}

// Write implements Sink.
func (c *Client) Write(ctx context.Context, req *Request) (*Result, error) {
	res, err := c.client.Write(ctx, connect.NewRequest(&auditv1.WriteRequest{
		Records:  req.Records,
		Delivery: req.Delivery,
	}))
	if err != nil {
		return nil, err
	}
	return fromProto(res.Msg), nil
}

// Handler serves SinkService from a Sink, which is how the writer is reached
// by anything that cannot call it in process.
type Handler struct {
	auditv1connect.UnimplementedSinkServiceHandler
	sink Sink
}

// NewHandler returns the path and handler to mount.
func NewHandler(s Sink, opts ...connect.HandlerOption) (string, http.Handler) {
	return auditv1connect.NewSinkServiceHandler(&Handler{sink: s}, opts...)
}

// Write implements the service.
func (h *Handler) Write(
	ctx context.Context, req *connect.Request[auditv1.WriteRequest],
) (*connect.Response[auditv1.WriteResponse], error) {
	res, err := h.sink.Write(ctx, &Request{
		Records:  req.Msg.GetRecords(),
		Delivery: req.Msg.GetDelivery(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(toProto(res)), nil
}

func toProto(r *Result) *auditv1.WriteResponse {
	if r == nil {
		return &auditv1.WriteResponse{}
	}
	out := &auditv1.WriteResponse{Accepted: int32(r.Accepted)}
	for _, x := range r.Rejected {
		out.Rejected = append(out.Rejected, &auditv1.Rejection{Id: x.ID, Reason: x.Reason})
	}
	return out
}

func fromProto(m *auditv1.WriteResponse) *Result {
	out := &Result{Accepted: int(m.GetAccepted())}
	for _, x := range m.GetRejected() {
		out.Rejected = append(out.Rejected, Rejection{ID: x.GetId(), Reason: x.GetReason()})
	}
	return out
}
