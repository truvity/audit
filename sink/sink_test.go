package sink_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	auditv1 "github.com/truvity/audit/gen/audit/v1"

	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

func one(id string) *record.Record {
	return &record.Record{
		Id: id, Source: "shop", Action: "shop.order.placed", TenantId: "acme",
		Operation: auditv1.Operation_OPERATION_CREATE,
		Outcome:   &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
	}
}

func TestParseDelivery(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want sink.Delivery
		ok   bool
	}{
		{"block", sink.Block, true},
		{"outbox", sink.Outbox, true},
		{"best_effort", sink.BestEffort, true},
		{"best-effort", sink.BestEffort, true},
		{"", sink.BestEffort, true},
		{" BLOCK ", sink.Block, true},
		{"eventually", 0, false},
	} {
		got, err := sink.ParseDelivery(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("ParseDelivery(%q) error = %v, want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("ParseDelivery(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// A sink never reports success for a record it did not keep, and a refusal is
// an error the caller can act on rather than a count it has to compare.
func TestResultErrNamesEveryRefusal(t *testing.T) {
	if err := (&sink.Result{Accepted: 2}).Err(); err != nil {
		t.Fatalf("an accepted batch is not an error: %v", err)
	}
	err := (&sink.Result{Rejected: []sink.Rejection{
		{ID: "a", Reason: "unknown catalogue version"},
		{ID: "b", Reason: "too large"},
	}}).Err()
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"a: unknown catalogue version", "b: too large", "refused 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestMemoryKeepsWhatItTook(t *testing.T) {
	m := &sink.Memory{}
	res, err := m.Write(context.Background(), &sink.Request{
		Records: []*record.Record{one("a"), one("b")}, Delivery: sink.Block,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 2 || m.Len() != 2 {
		t.Fatalf("accepted %d, holding %d", res.Accepted, m.Len())
	}
	if got := m.Records()[0].GetId(); got != "a" {
		t.Fatalf("records are not in order: first is %q", got)
	}
	m.Reset()
	if m.Len() != 0 {
		t.Fatal("reset must forget everything")
	}

	m.Fail = errors.New("no")
	if _, err := m.Write(context.Background(), &sink.Request{Records: []*record.Record{one("c")}}); err == nil {
		t.Fatal("a failing sink must say so")
	}
}

func TestMemoryHonoursItsLimit(t *testing.T) {
	m := &sink.Memory{Limit: 2}
	for _, id := range []string{"a", "b", "c"} {
		if _, err := m.Write(context.Background(), &sink.Request{Records: []*record.Record{one(id)}}); err != nil {
			t.Fatal(err)
		}
	}
	held := m.Records()
	if len(held) != 2 || held[0].GetId() != "b" || held[1].GetId() != "c" {
		t.Fatalf("holding %d records, oldest first: %v", len(held), ids(held))
	}
}

// The same contract on both sides of a process boundary is what lets a
// deployment put a queue in the middle, or take it away, without either end
// knowing.
func TestConnectRoundTrip(t *testing.T) {
	store := &sink.Memory{}
	path, handler := sink.NewHandler(store)
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

// A refusal on the far side reaches the caller as a refusal, not as silence.
func TestConnectCarriesRefusals(t *testing.T) {
	refusing := sink.Func(func(_ context.Context, req *sink.Request) (*sink.Result, error) {
		return &sink.Result{Rejected: []sink.Rejection{{ID: req.Records[0].GetId(), Reason: "unknown catalogue version"}}}, nil
	})
	path, handler := sink.NewHandler(refusing)
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
	path, handler := sink.NewHandler(&sink.Memory{Fail: errors.New("the store is unreachable")})
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

func ids(rs []*record.Record) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.GetId())
	}
	return out
}
