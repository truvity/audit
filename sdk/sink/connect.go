package sink

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/trace"

	auditv1 "github.com/truvity/audit/sdk/gen/audit/v1"
	"github.com/truvity/audit/sdk/gen/audit/v1/auditv1connect"
	"github.com/truvity/audit/sdk/telemetry"
)

// Client is a Sink that calls SinkService on another process: what an adapter
// outside the cluster uses, and what a queue consumer calls on the writer.
type Client struct {
	client auditv1connect.SinkServiceClient
	best   Durability
}

// NewClient returns a Sink backed by the SinkService at baseURL.
func NewClient(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	// The call carries the caller's trace, when there is one: an interceptor
	// that does nothing without a tracer provider. A caller's own options come
	// after and may add to it.
	opts = append(telemetry.ConnectClientOptions(), opts...)
	return &Client{client: auditv1connect.NewSinkServiceClient(httpClient, baseURL, opts...)}
}

// Expecting names the strongest durability the service at the other end is
// configured to give, which a client cannot learn before it writes. It is what
// Require reads at start-up; every write is still checked by Guard against what
// the service actually reports.
func (c *Client) Expecting(d Durability) *Client {
	c.best = d
	return c
}

// Guarantees implements Guarantor, as far as Expecting said.
func (c *Client) Guarantees() Durability { return c.best }

// Write implements Sink.
func (c *Client) Write(ctx context.Context, req *Request) (*Result, error) {
	ctx, done := Observe(ctx, TransportConnectClient, trace.SpanKindClient, req)
	res, err := c.write(ctx, req)
	done(res, err)
	return res, err
}

func (c *Client) write(ctx context.Context, req *Request) (*Result, error) {
	res, err := c.client.Write(ctx, connect.NewRequest(&auditv1.WriteRequest{
		Records:  req.Records,
		Delivery: req.Delivery,
	}))
	if err != nil {
		return nil, err
	}
	return fromProto(res.Msg), nil
}

func fromProto(m *auditv1.WriteResponse) *Result {
	out := &Result{Accepted: int(m.GetAccepted()), Durability: m.GetDurability()}
	for _, x := range m.GetRejected() {
		out.Rejected = append(out.Rejected, Rejection{ID: x.GetId(), Reason: x.GetReason()})
	}
	return out
}
