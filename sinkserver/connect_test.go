package sinkserver_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	auditv1 "github.com/truvity/audit/sdk/gen/audit/v1"
	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/sdk/sink"
	"github.com/truvity/audit/sdk/sink/logsink"
	"github.com/truvity/audit/sdk/sink/sinktest"
	"github.com/truvity/audit/sinkserver"
)

func one(id string) *record.Record {
	return &record.Record{
		Id: id, Source: "shop", Action: "shop.order.placed", TenantId: "acme",
		Operation: auditv1.Operation_OPERATION_CREATE,
		Outcome:   &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
	}
}

// declares is a sink that says what it can give and what it did give.
type declares struct {
	best, says sink.Durability
	err        error
}

func (d *declares) Guarantees() sink.Durability { return d.best }

func (d *declares) Write(_ context.Context, req *sink.Request) (*sink.Result, error) {
	if d.err != nil {
		return nil, d.err
	}
	return &sink.Result{Accepted: len(req.Records), Durability: d.says}, nil
}

func TestConnectRoundTrip(t *testing.T) {
	store := &sink.Memory{}
	path, handler := sinkserver.NewHandler(store)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()

	client := sink.NewClient(server.Client(), server.URL)
	res, err := client.Write(context.Background(), &sink.Request{
		Records: []*record.Record{one("a"), one("b")}, Delivery: sink.Block,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 2 {
		t.Fatalf("accepted %d, want 2", res.Accepted)
	}
	if store.Len() != 2 {
		t.Fatalf("the far side holds %d records", store.Len())
	}
	if got := store.Records()[0].GetAction(); got != "shop.order.placed" {
		t.Fatalf("the record did not survive the crossing: action %q", got)
	}
}

func TestConnectCarriesRefusals(t *testing.T) {
	refusing := sink.Func(func(_ context.Context, req *sink.Request) (*sink.Result, error) {
		return &sink.Result{Rejected: []sink.Rejection{{ID: req.Records[0].GetId(), Reason: "unknown catalogue version"}}}, nil
	})
	path, handler := sinkserver.NewHandler(refusing)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()

	res, err := sink.NewClient(server.Client(), server.URL).Write(
		context.Background(), &sink.Request{Records: []*record.Record{one("a")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Err(); err == nil || !strings.Contains(err.Error(), "unknown catalogue version") {
		t.Fatalf("the refusal did not cross: %v", err)
	}
}

func TestConnectReportsAFailingSink(t *testing.T) {
	path, handler := sinkserver.NewHandler(&sink.Memory{Fail: errors.New("the store is unreachable")})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()

	_, err := sink.NewClient(server.Client(), server.URL).Write(
		context.Background(), &sink.Request{Records: []*record.Record{one("a")}})
	if err == nil {
		t.Fatal("a sink that cannot take records must say so across the wire")
	}
}

func TestReceiverGuaranteesWhatItsNextHopDoes(t *testing.T) {
	r := &sinkserver.Receiver{To: &sink.Memory{}}
	if got := sink.Guarantees(r); got != sink.Logged {
		t.Errorf("Guarantees = %v, want the next hop's", got)
	}
	if got := sink.Guarantees(&sinkserver.Receiver{}); got != sink.Unspecified {
		t.Errorf("a receiver with no next hop guarantees %v", got)
	}
}

func TestConnectPairConforms(t *testing.T) {
	sinktest.Run(t, func(t *testing.T) sinktest.Subject {
		// A record the suite would use to provoke a refusal cannot even be
		// marshalled by the client; TestConnectCarriesRefusals covers a
		// refusal from the far side.
		far := logsink.New(logsink.Options{Out: io.Discard})
		path, handler := sinkserver.NewHandler(far)
		mux := http.NewServeMux()
		mux.Handle(path, handler)
		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)
		client := sink.NewClient(server.Client(), server.URL).Expecting(sink.Logged)
		return sinktest.Subject{Sink: client, Durability: sink.Logged}
	})
}

func TestConnectCarriesDurability(t *testing.T) {
	path, handler := sinkserver.NewHandler(&declares{best: sink.Archived, says: sink.Archived})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()

	client := sink.NewClient(server.Client(), server.URL)
	if sink.Require(client, sink.Logged) == nil {
		t.Error("a client that was told nothing guaranteed something")
	}
	res, err := client.Write(context.Background(), &sink.Request{Records: sinktest.Records(1)})
	if err != nil || res.Durability != sink.Archived {
		t.Fatalf("Write = %+v, %v", res, err)
	}
}
