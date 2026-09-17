package emit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvity/audit/emit"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

func request(remote string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/orders/1", nil)
	r.RemoteAddr = remote
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// Getting the trusted hop count wrong is how an audit trail comes to record a
// load balancer as the actor's address.
func TestClientChainRemovesYourOwnEdge(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remote     string
		forwarded  string
		hops       int
		wantClient string
		wantChain  []string
	}{
		{
			name:   "no proxy: the peer is the client",
			remote: "203.0.113.7:51000", hops: 0,
			wantClient: "203.0.113.7", wantChain: []string{"203.0.113.7"},
		},
		{
			name:   "one hop: the entry before it is the client",
			remote: "10.0.0.5:8080", forwarded: "203.0.113.7", hops: 1,
			wantClient: "203.0.113.7", wantChain: []string{"203.0.113.7"},
		},
		{
			name:   "two hops of our own edge",
			remote: "10.0.0.5:8080", forwarded: "203.0.113.7, 198.51.100.2", hops: 2,
			wantClient: "203.0.113.7", wantChain: []string{"203.0.113.7"},
		},
		{
			name:   "the hearsay before our edge is kept",
			remote: "10.0.0.5:8080", forwarded: "198.51.100.9, 203.0.113.7", hops: 1,
			wantClient: "203.0.113.7", wantChain: []string{"198.51.100.9", "203.0.113.7"},
		},
		{
			name:   "more hops claimed than the chain has",
			remote: "10.0.0.5:8080", forwarded: "203.0.113.7", hops: 9,
			wantClient: "10.0.0.5", wantChain: []string{"203.0.113.7", "10.0.0.5"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{}
			if tc.forwarded != "" {
				headers["X-Forwarded-For"] = tc.forwarded
			}
			c := emit.FromRequest(request(tc.remote, headers), tc.hops)
			if got := emit.Client(c); got != tc.wantClient {
				t.Errorf("client = %q, want %q (chain %v)", got, tc.wantClient, c.GetClientAddresses())
			}
			if got := strings.Join(c.GetClientAddresses(), ","); got != strings.Join(tc.wantChain, ",") {
				t.Errorf("chain = %v, want %v", c.GetClientAddresses(), tc.wantChain)
			}
		})
	}
}

func TestFromRequestReadsTheRest(t *testing.T) {
	c := emit.FromRequest(request("10.0.0.5:8080", map[string]string{
		"User-Agent":   "curl/8.5.0",
		"X-Request-Id": "req_01J8",
		"traceparent":  "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}), 0)

	if c.GetUserAgent() != "curl/8.5.0" {
		t.Errorf("user agent = %q", c.GetUserAgent())
	}
	if c.GetRequestId() != "req_01J8" {
		t.Errorf("request id = %q", c.GetRequestId())
	}
	if c.GetTraceId() != "4bf92f3577b34da6a3ce929d0e0e4736" || c.GetSpanId() != "00f067aa0ba902b7" {
		t.Errorf("trace = %q span = %q", c.GetTraceId(), c.GetSpanId())
	}
}

// A wrong correlation identifier costs more than a missing one.
func TestTraceParentIsAllOrNothing(t *testing.T) {
	for _, bad := range []string{
		"", "nonsense", "00-tooshort-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
	} {
		c := emit.FromRequest(request("10.0.0.5:1", map[string]string{"traceparent": bad}), 0)
		if c.GetTraceId() != "" || c.GetSpanId() != "" {
			t.Errorf("traceparent %q yielded trace %q span %q", bad, c.GetTraceId(), c.GetSpanId())
		}
	}
}

// A handler emits without passing provenance along by hand.
func TestMiddlewareReachesTheRecord(t *testing.T) {
	store := &sink.Memory{}
	e := emitter(t, store, emit.Hooks{})

	var emitErr error
	handler := emit.Middleware(1)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		emitErr = e.Record(r.Context(), placed())
	}))
	req := request("10.0.0.5:8080", map[string]string{
		"X-Forwarded-For": "203.0.113.7",
		"User-Agent":      "curl/8.5.0",
	})
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if emitErr != nil {
		t.Fatal(emitErr)
	}

	written := store.Records()
	if len(written) != 1 {
		t.Fatalf("%d records written", len(written))
	}
	c := written[0].GetContext()
	if emit.Client(c) != "203.0.113.7" || c.GetUserAgent() != "curl/8.5.0" {
		t.Fatalf("the record did not carry the request's provenance: %+v", c)
	}
}

// Several records of one request must not share a value that normalising will
// trim, or the first long user agent would shorten every later record's.
func TestRecordsOfOneRequestDoNotShareProvenance(t *testing.T) {
	store := &sink.Memory{}
	e := emitter(t, store, emit.Hooks{})

	ctx := emit.WithRequest(context.Background(), emit.FromRequest(
		request("10.0.0.5:8080", map[string]string{
			"X-Forwarded-For": "203.0.113.7",
			"User-Agent":      strings.Repeat("a", 400),
		}), 1))

	for i := 0; i < 2; i++ {
		if err := e.Record(ctx, placed()); err != nil {
			t.Fatal(err)
		}
	}
	carried := emit.RequestContext(ctx)
	if len(carried.GetUserAgent()) != 400 {
		t.Fatalf("the request's own value was trimmed to %d: a record borrowed it instead of copying",
			len(carried.GetUserAgent()))
	}
	for i, r := range store.Records() {
		if n := len(r.GetContext().GetUserAgent()); n != record.Default.UserAgentLen {
			t.Fatalf("record %d carries a %d character user agent, want it trimmed to %d",
				i, n, record.Default.UserAgentLen)
		}
	}
}

// A handler that knows better than the middleware keeps what it set.
func TestACallerMayProvideItsOwnProvenance(t *testing.T) {
	store := &sink.Memory{}
	e := emitter(t, store, emit.Hooks{})

	ctx := emit.WithRequest(context.Background(), emit.FromRequest(
		request("10.0.0.5:8080", map[string]string{"X-Forwarded-For": "203.0.113.7"}), 1))

	r := placed()
	r.Context = &record.Context{ClientAddresses: []string{"192.0.2.1"}}
	if err := e.Record(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := emit.Client(store.Records()[0].GetContext()); got != "192.0.2.1" {
		t.Fatalf("client = %q, want the caller's own value", got)
	}
}
