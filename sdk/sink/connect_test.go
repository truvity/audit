package sink_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	auditv1 "github.com/truvity/audit/sdk/gen/audit/v1"
	"github.com/truvity/audit/sdk/gen/audit/v1/auditv1connect"
	"github.com/truvity/audit/sdk/sink"
)

// stub is the far side of the client, written against the generated handler so
// that the SDK's tests need nothing from the server module.
type stub struct {
	auditv1connect.UnimplementedSinkServiceHandler
	got *auditv1.WriteRequest
}

func (s *stub) Write(_ context.Context, req *connect.Request[auditv1.WriteRequest]) (*connect.Response[auditv1.WriteResponse], error) {
	s.got = req.Msg
	return connect.NewResponse(&auditv1.WriteResponse{
		Accepted:   int32(len(req.Msg.GetRecords())),
		Durability: sink.Queued,
		Rejected:   []*auditv1.Rejection{{Id: "bad", Reason: "no"}},
	}), nil
}

func TestClientCarriesTheBatchAndTheAnswer(t *testing.T) {
	far := &stub{}
	path, handler := auditv1connect.NewSinkServiceHandler(far)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := sink.NewClient(server.Client(), server.URL).Expecting(sink.Queued)
	if err := sink.Require(client, sink.Queued); err != nil {
		t.Fatal(err)
	}
	res, err := client.Write(context.Background(), &sink.Request{Records: []*auditv1.Record{one("a")}, Delivery: sink.Block})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 || res.Durability != sink.Queued || len(res.Rejected) != 1 || res.Rejected[0].ID != "bad" {
		t.Fatalf("Write = %+v", res)
	}
	if len(far.got.GetRecords()) != 1 || far.got.GetDelivery() != sink.Block {
		t.Fatalf("the far side saw %+v", far.got)
	}
}
