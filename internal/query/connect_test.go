package query_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"errors"

	"connectrpc.com/connect"

	"github.com/truvity/audit/auth"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/gen/audit/v1/auditv1connect"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/internal/query"
)

// served puts the handler behind a real server and returns a real client, so
// that what is tested is the wire and not a Go call dressed as one.
func served(t *testing.T, g auth.Grant, as auth.Principal) auditv1connect.QueryServiceClient {
	t.Helper()
	s, _ := service(t, g)
	path, handler := query.NewHandler(s, auth.None{As: as})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return auditv1connect.NewQueryServiceClient(srv.Client(), srv.URL)
}

func TestOverTheWire(t *testing.T) {
	client := served(t, fullGrant(), caller())
	ctx := context.Background()

	res, err := client.Search(ctx, connect.NewRequest(&auditv1.SearchRequest{
		Profile: "security", Limit: 2,
		Sort: []*auditv1.Sort{{Field: auditv1.Sort_FIELD_OCCURRED_AT, Order: auditv1.Sort_ORDER_ASC}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.GetItems()) != 2 {
		t.Fatalf("%d items", len(res.Msg.GetItems()))
	}
	first := res.Msg.GetItems()[0]
	if first.GetId() == "" || first.GetTenantId() == "" {
		t.Fatalf("an item came back empty: %+v", first)
	}
	// The short spellings the index keeps are enums again on the wire.
	if first.GetOperation() != auditv1.Operation_OPERATION_CREATE {
		t.Fatalf("operation %v, want create", first.GetOperation())
	}
	if first.GetOutcome().GetResult() != auditv1.Outcome_RESULT_SUCCESS {
		t.Fatalf("outcome %v", first.GetOutcome())
	}
	if res.Msg.GetCursors().GetNext() == "" {
		t.Fatal("no next cursor over the wire")
	}

	// And the cursor pages.
	next, err := client.Search(ctx, connect.NewRequest(&auditv1.SearchRequest{
		Profile: "security", Limit: 2, Cursor: res.Msg.GetCursors().GetNext(),
		Sort: []*auditv1.Sort{{Field: auditv1.Sort_FIELD_OCCURRED_AT, Order: auditv1.Sort_ORDER_ASC}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Msg.GetItems()) != 2 {
		t.Fatalf("%d items on the second page", len(next.Msg.GetItems()))
	}
	if next.Msg.GetItems()[0].GetId() == first.GetId() {
		t.Fatal("the second page repeated the first")
	}
	if next.Msg.GetCursors().GetPrev() == "" {
		t.Fatal("a page reached by a cursor has no way back")
	}
}

func TestFacetsAndGetOverTheWire(t *testing.T) {
	client := served(t, fullGrant(), caller())
	ctx := context.Background()

	facets, err := client.Facets(ctx, connect.NewRequest(&auditv1.FacetsRequest{
		Profile: "security", Fields: []string{index.FieldAction},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(facets.Msg.GetFacets()) != 1 || len(facets.Msg.GetFacets()[0].GetValues()) == 0 {
		t.Fatalf("facets: %+v", facets.Msg.GetFacets())
	}

	got, err := client.Get(ctx, connect.NewRequest(&auditv1.GetRequest{
		Profile: "security", Id: "018f0000-0000-7000-8000-00000000000a",
	}))
	if err != nil {
		t.Fatal(err)
	}
	// Provenance is the point of Get: an answer a reader can check against the
	// copy the digest chain accounts for.
	if got.Msg.GetProvenance().GetObjectKey() == "" || got.Msg.GetProvenance().GetLine() == 0 {
		t.Fatalf("no provenance: %+v", got.Msg.GetProvenance())
	}
}

// A denial must reach the client as a denial, or a client retries it forever.
func TestADenialIsNotAServerFault(t *testing.T) {
	client := served(t, auth.Grant{
		Tenants: []string{"acme"}, Profiles: []string{"history"},
		Operations: []auth.Operation{auth.Search},
	}, caller())

	_, err := client.Search(context.Background(), connect.NewRequest(&auditv1.SearchRequest{
		Profile: "security", Limit: 10,
	}))
	if err == nil {
		t.Fatal("a profile outside the grant was answered")
	}
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a denial came back as %v", connect.CodeOf(err))
	}
}

// A caller the authenticator could not name is denied, rather than answered as
// somebody. This is what `none` means: it invents no identity.
func TestAnUnidentifiedCallerIsDenied(t *testing.T) {
	client := served(t, fullGrant(), auth.Principal{})
	_, err := client.Search(context.Background(), connect.NewRequest(&auditv1.SearchRequest{
		Profile: "security", Limit: 10,
	}))
	if err == nil {
		t.Fatal("a caller with no identity was answered")
	}
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("code %v", connect.CodeOf(err))
	}
}

// A mismatched cursor is the caller's to fix, not the server's to blame.
func TestAMismatchedCursorIsInvalidArgument(t *testing.T) {
	client := served(t, fullGrant(), caller())
	_, err := client.Search(context.Background(), connect.NewRequest(&auditv1.SearchRequest{
		Profile: "security", Cursor: "not-a-cursor",
	}))
	if err == nil {
		t.Fatal("a made-up cursor was accepted")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code %v", connect.CodeOf(err))
	}
}

// A searcher that is down is not the caller's fault. Calling it
// invalid_argument tells a well-behaved client never to try again, which turns
// a database restart into an outage that outlives it.
func TestASearcherFailureIsUnavailableNotTheCallersFault(t *testing.T) {
	s, _ := service(t, fullGrant())
	s.Searcher = broken{}
	path, handler := query.NewHandler(s, auth.None{As: caller()})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := auditv1connect.NewQueryServiceClient(srv.Client(), srv.URL)
	_, err := client.Search(context.Background(), connect.NewRequest(&auditv1.SearchRequest{
		Profile: "security", Limit: 10,
	}))
	if err == nil {
		t.Fatal("a broken searcher answered")
	}
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("a searcher outage came back as %v, which tells a client not to retry",
			connect.CodeOf(err))
	}
}

// broken stands in for a searcher whose database is down.
type broken struct{}

func (broken) Search(context.Context, index.Query) (index.Page, error) {
	return index.Page{}, errors.New("dial tcp: connection refused")
}
func (broken) Facets(context.Context, index.Query, []string, int) ([]index.Facet, error) {
	return nil, errors.New("dial tcp: connection refused")
}
func (broken) Get(context.Context, string, string) (index.Row, index.Provenance, error) {
	return index.Row{}, index.Provenance{}, errors.New("dial tcp: connection refused")
}
func (broken) Capabilities() index.Capabilities { return index.Capabilities{MaxConjunctions: 4} }

// The bounds the reference publishes are the service's, so the answer to "is
// this too much" does not depend on which searcher is configured.
func TestPublishedLimitsAreEnforcedAndSaidSo(t *testing.T) {
	client := served(t, fullGrant(), caller())
	ctx := context.Background()

	many := make([]string, 101)
	for i := range many {
		many[i] = "x"
	}
	for _, c := range []struct {
		name string
		req  *auditv1.SearchRequest
	}{
		{"too many values in an in", &auditv1.SearchRequest{
			Profile: "security",
			Filter: []*auditv1.Filter{{Action: &auditv1.StringPredicate{
				Operator: &auditv1.StringPredicate_In{In: &auditv1.StringList{Values: many}}}}}}},
		{"too many sort terms", &auditv1.SearchRequest{
			Profile: "security",
			Sort: []*auditv1.Sort{
				{Field: auditv1.Sort_FIELD_ACTION}, {Field: auditv1.Sort_FIELD_SOURCE},
				{Field: auditv1.Sort_FIELD_TENANT_ID}, {Field: auditv1.Sort_FIELD_ID},
				{Field: auditv1.Sort_FIELD_OCCURRED_AT},
			}}},
		{"too many conjunctions", &auditv1.SearchRequest{
			Profile: "security", Filter: make([]*auditv1.Filter, 5)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := client.Search(ctx, connect.NewRequest(c.req))
			if err == nil {
				t.Fatal("want a refusal")
			}
			if connect.CodeOf(err) != connect.CodeResourceExhausted {
				t.Fatalf("a limit came back as %v, want resource_exhausted", connect.CodeOf(err))
			}
		})
	}
}

// `first` is a real cursor meaning this question from the beginning, so a
// caller holds one kind of cursor rather than two.
func TestFirstCursorReturnsToTheBeginning(t *testing.T) {
	client := served(t, fullGrant(), caller())
	ctx := context.Background()
	req := &auditv1.SearchRequest{
		Profile: "security", Limit: 1,
		Sort: []*auditv1.Sort{{Field: auditv1.Sort_FIELD_OCCURRED_AT, Order: auditv1.Sort_ORDER_ASC}},
	}
	one, err := client.Search(ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	if one.Msg.GetCursors().GetFirst() == "" {
		t.Fatal("no first cursor, so a caller cannot return to page one")
	}

	// Page on, then go back to the first.
	req.Cursor = one.Msg.GetCursors().GetNext()
	if _, err := client.Search(ctx, connect.NewRequest(req)); err != nil {
		t.Fatal(err)
	}
	req.Cursor = one.Msg.GetCursors().GetFirst()
	back, err := client.Search(ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	if back.Msg.GetItems()[0].GetId() != one.Msg.GetItems()[0].GetId() {
		t.Fatalf("first returned %s, the first page was %s",
			back.Msg.GetItems()[0].GetId(), one.Msg.GetItems()[0].GetId())
	}
}
