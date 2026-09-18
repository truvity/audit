package query_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/catalogue"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/internal/query"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

func at(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.UTC()
}

// reads collects what the service recorded about itself.
type reads struct{ records []*record.Record }

func (c *reads) Write(_ context.Context, req *sink.Request) (*sink.Result, error) {
	c.records = append(c.records, req.Records...)
	return &sink.Result{Accepted: len(req.Records)}, nil
}

func (c *reads) of(action string) []*record.Record {
	var out []*record.Record
	for _, r := range c.records {
		if r.GetAction() == action {
			out = append(out, r)
		}
	}
	return out
}

// corpus is two tenants in one profile.
func corpus(t *testing.T) *index.Memory {
	t.Helper()
	m := index.NewMemory()
	base := at(t, "2026-09-17T10:00:00Z")
	var rows []index.Row
	for n, tenant := range []string{"acme", "acme", "globex", "globex"} {
		rows = append(rows, index.Row{
			ID:       "018f0000-0000-7000-8000-00000000000" + string("abcdef"[n]),
			TenantID: tenant, OccurredAt: base.Add(time.Duration(n) * time.Minute),
			RecordedAt: base.Add(time.Duration(n) * time.Minute),
			Source:     "wallet", Action: "wallet.credential.issued",
			Operation: "create", Outcome: "success",
			ObjectKey: "k", Line: n + 1,
		})
	}
	if err := m.Index(context.Background(), "security", rows); err != nil {
		t.Fatal(err)
	}
	return m
}

func service(t *testing.T, g auth.Grant) (*query.Service, *reads) {
	t.Helper()
	common, err := catalogue.Common()
	if err != nil {
		t.Fatal(err)
	}
	into := &reads{}
	s, err := query.New(&query.Service{
		Searcher:   corpus(t),
		Authorizer: auth.Declarative{Rules: []auth.Rule{{Name: "a-rule", Grant: g}}},
		Sink:       into, Catalogue: common, Instance: "query-1",
		OnUnrecorded: func(action string, err error) {
			t.Errorf("the read of %s was not recorded: %v", action, err)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, into
}

func caller() auth.Principal {
	return auth.Principal{Issuer: "https://issuer.test", Subject: "olga", Via: "jwt"}
}

func fullGrant() auth.Grant {
	return auth.Grant{
		AllTenants: true, Profiles: []string{"security"},
		Operations: []auth.Operation{auth.Search, auth.Facets, auth.Get},
	}
}

// The grant becomes a term in the query, so there is no path from a request to
// a row outside it.
func TestTheGrantNarrowsTheSearch(t *testing.T) {
	s, _ := service(t, auth.Grant{
		Tenants: []string{"acme"}, Profiles: []string{"security"},
		Operations: []auth.Operation{auth.Search},
	})
	page, _, err := s.Search(context.Background(), caller(),
		&auditv1.SearchRequest{Profile: "security", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("%d rows, want the 2 of the granted tenant", len(page.Rows))
	}
	for _, r := range page.Rows {
		if r.TenantID != "acme" {
			t.Fatalf("a row of %s came back under a grant for acme", r.TenantID)
		}
	}
}

// A caller cannot widen past the grant by asking for another tenant.
func TestAskingForAnotherTenantDoesNotWiden(t *testing.T) {
	s, _ := service(t, auth.Grant{
		Tenants: []string{"acme"}, Profiles: []string{"security"},
		Operations: []auth.Operation{auth.Search},
	})
	page, _, err := s.Search(context.Background(), caller(), &auditv1.SearchRequest{
		Profile: "security", Limit: 100,
		Filter: []*auditv1.Filter{{
			TenantId: &auditv1.StringPredicate{
				Operator: &auditv1.StringPredicate_Equal{Equal: "globex"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 0 {
		t.Fatalf("asking for another tenant returned %d rows", len(page.Rows))
	}
}

// A grant's window narrows a wider request rather than refusing it.
func TestTheGrantsWindowClampsTheRequest(t *testing.T) {
	g := fullGrant()
	g.From = at(t, "2026-09-17T10:02:00Z")
	s, _ := service(t, g)

	page, _, err := s.Search(context.Background(), caller(),
		&auditv1.SearchRequest{Profile: "security", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("%d rows, want the 2 inside the grant's window", len(page.Rows))
	}
}

// A profile or operation outside the grant is refused, saying which.
func TestOutsideTheGrantIsRefused(t *testing.T) {
	s, into := service(t, auth.Grant{
		Tenants: []string{"acme"}, Profiles: []string{"history"},
		Operations: []auth.Operation{auth.Search},
	})
	_, _, err := s.Search(context.Background(), caller(),
		&auditv1.SearchRequest{Profile: "security", Limit: 10})
	if !errors.Is(err, auth.ErrDenied) {
		t.Fatalf("a profile outside the grant gave %v", err)
	}
	if len(into.of("audit.search")) != 0 {
		t.Fatal("a request refused before it ran was recorded as a search")
	}
}

// Reads of an audit trail are themselves auditable: a trail that shows what
// everyone did except who looked at it is missing where an investigation starts.
func TestEveryReadRecordsItself(t *testing.T) {
	s, into := service(t, fullGrant())
	ctx := context.Background()

	if _, _, err := s.Search(ctx, caller(),
		&auditv1.SearchRequest{Profile: "security", Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Facets(ctx, caller(), &auditv1.FacetsRequest{
		Profile: "security", Fields: []string{index.FieldAction}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Get(ctx, caller(), &auditv1.GetRequest{
		Profile: "security", Id: "018f0000-0000-7000-8000-00000000000a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	for _, action := range []string{"audit.search", "audit.facets", "audit.get"} {
		got := into.of(action)
		if len(got) != 1 {
			t.Fatalf("%d records of %s, want 1", len(got), action)
		}
		if got[0].GetActor().GetId() != "olga" {
			t.Fatalf("%s does not name the caller: %+v", action, got[0].GetActor())
		}
		// The rule that allowed it, so that a read can be traced to a rule.
		if got[0].GetOutcome().GetReason() != "a-rule" {
			t.Fatalf("%s does not name the rule that allowed it: %+v", action, got[0].GetOutcome())
		}
		// And how the caller authenticated: the same subject from a gateway
		// and from a bearer token are different assurances.
		if got[0].GetActor().GetAuthMethod() != "jwt" {
			t.Fatalf("%s does not say how the caller authenticated: %+v", action, got[0].GetActor())
		}
	}
}

// "Who read this tenant's records" is a target lookup, not a reconstruction
// from grants — so a read narrowed to tenants names them.
func TestANarrowedReadNamesItsTenants(t *testing.T) {
	s, into := service(t, auth.Grant{
		Tenants: []string{"acme"}, Profiles: []string{"security"},
		Operations: []auth.Operation{auth.Search},
	})
	if _, _, err := s.Search(context.Background(), caller(),
		&auditv1.SearchRequest{Profile: "security", Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got := into.of("audit.search")
	if len(got) != 1 {
		t.Fatalf("%d search records", len(got))
	}
	var tenants []string
	for _, tg := range got[0].GetTargets() {
		if tg.GetType() == "tenant" {
			tenants = append(tenants, tg.GetId())
		}
	}
	if len(tenants) != 1 || tenants[0] != "acme" {
		t.Fatalf("the read names tenants %v, want [acme]", tenants)
	}
}

// A refused read is recorded too. An attempt to read the trail is a fact about
// who was looking, and the refused one is the more interesting of the two.
func TestARefusedReadIsRecorded(t *testing.T) {
	s, into := service(t, fullGrant())
	_, _, _, err := s.Get(context.Background(), caller(), &auditv1.GetRequest{
		Profile: "security", Id: "018f0000-0000-7000-8000-0000000000ff"})
	if err == nil {
		t.Fatal("a record that is not there was found")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got := into.of("audit.get")
	if len(got) != 1 {
		t.Fatalf("%d records of a failed get", len(got))
	}
	if got[0].GetOutcome().GetResult() != auditv1.Outcome_RESULT_FAILURE {
		t.Fatalf("a failed read was recorded as a success: %+v", got[0].GetOutcome())
	}
}

// A record the grant does not cover is reported as absent, not as forbidden:
// the two are the same answer to someone who should not know it exists.
func TestAGetOutsideTheGrantLooksLikeAbsence(t *testing.T) {
	s, _ := service(t, auth.Grant{
		Tenants: []string{"acme"}, Profiles: []string{"security"},
		Operations: []auth.Operation{auth.Get},
	})
	_, _, _, err := s.Get(context.Background(), caller(), &auditv1.GetRequest{
		Profile: "security", Id: "018f0000-0000-7000-8000-00000000000c"})
	if err == nil {
		t.Fatal("a record of another tenant was returned")
	}
	if strings.Contains(strings.ToLower(err.Error()), "denied") ||
		strings.Contains(strings.ToLower(err.Error()), "grant") {
		t.Fatalf("the refusal confirms the record exists: %v", err)
	}
}

// A service without an authorizer would answer everything.
func TestNewRefusesWithoutAnAuthorizer(t *testing.T) {
	_, err := query.New(&query.Service{Searcher: corpus(t)})
	if err == nil {
		t.Fatal("a service with no authorizer was accepted")
	}
	if !strings.Contains(err.Error(), "answer everything") {
		t.Errorf("the refusal should say why: %v", err)
	}
}

// An unbounded OR is unbounded work, and the refusal is the service's rather
// than whichever searcher happens to be configured.
func TestTooManyConjunctionsAreRefusedByTheService(t *testing.T) {
	s, _ := service(t, fullGrant())
	_, _, err := s.Search(context.Background(), caller(), &auditv1.SearchRequest{
		Profile: "security", Filter: make([]*auditv1.Filter, 5)})
	if err == nil {
		t.Fatal("five conjunctions were accepted")
	}
}

// Every time operator becomes a half-open range, so an index can answer it
// without reading rows it will discard.
func TestTimeOperatorsBecomeRanges(t *testing.T) {
	s, _ := service(t, fullGrant())
	page, _, err := s.Search(context.Background(), caller(), &auditv1.SearchRequest{
		Profile: "security", Limit: 100,
		Filter: []*auditv1.Filter{{
			OccurredAt: &auditv1.TimePredicate{
				Operator: &auditv1.TimePredicate_GreaterThanOrEqual{
					GreaterThanOrEqual: timestamppb.New(at(t, "2026-09-17T10:02:00Z"))}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("%d rows for >= the third minute, want 2", len(page.Rows))
	}
}

// Paging over the wire: a cursor from a response resumes the next page, and
// every row comes back once.
func TestCursorsPageOverTheWire(t *testing.T) {
	s, _ := service(t, fullGrant())
	ctx := context.Background()
	req := &auditv1.SearchRequest{
		Profile: "security", Limit: 1,
		Sort: []*auditv1.Sort{{Field: auditv1.Sort_FIELD_OCCURRED_AT, Order: auditv1.Sort_ORDER_ASC}},
	}

	var seen []string
	for page := 0; page < 6; page++ {
		got, g, err := s.Search(ctx, caller(), req)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range got.Rows {
			seen = append(seen, r.ID)
		}
		cursors, err := s.Cursors(req, g, got)
		if err != nil {
			t.Fatal(err)
		}
		if cursors.GetNext() == "" {
			t.Fatal("a page carried no next cursor, so a tail could not resume")
		}
		if !got.More {
			break
		}
		req.Cursor = cursors.GetNext()
	}
	if len(seen) != 4 {
		t.Fatalf("paged %d rows of 4: %v", len(seen), seen)
	}
	unique := map[string]bool{}
	for _, id := range seen {
		if unique[id] {
			t.Fatalf("a row came back on two pages: %v", seen)
		}
		unique[id] = true
	}
}

// A cursor replayed against a different question is refused, rather than
// resuming from a position in an ordering that no longer exists.
func TestACursorIsBoundToItsQuery(t *testing.T) {
	s, _ := service(t, fullGrant())
	ctx := context.Background()
	first := &auditv1.SearchRequest{Profile: "security", Limit: 1}

	page, g, err := s.Search(ctx, caller(), first)
	if err != nil {
		t.Fatal(err)
	}
	cursors, err := s.Cursors(first, g, page)
	if err != nil {
		t.Fatal(err)
	}

	// The same cursor, a different filter.
	changed := &auditv1.SearchRequest{
		Profile: "security", Limit: 1, Cursor: cursors.GetNext(),
		Filter: []*auditv1.Filter{{
			Action: &auditv1.StringPredicate{
				Operator: &auditv1.StringPredicate_Equal{Equal: "wallet.credential.issued"}}}},
	}
	if _, _, err := s.Search(ctx, caller(), changed); !errors.Is(err, query.ErrCursorMismatch) {
		t.Fatalf("a cursor from another query gave %v", err)
	}

	// And the same cursor under a narrower grant, which is a different
	// question because the narrowing is part of it.
	narrow, _ := service(t, auth.Grant{
		Tenants: []string{"acme"}, Profiles: []string{"security"},
		Operations: []auth.Operation{auth.Search},
	})
	replay := &auditv1.SearchRequest{Profile: "security", Limit: 1, Cursor: cursors.GetNext()}
	if _, _, err := narrow.Search(ctx, caller(), replay); !errors.Is(err, query.ErrCursorMismatch) {
		t.Fatalf("a cursor issued under a wider grant was accepted: %v", err)
	}
}

// Asking for a different page size is the same question: a caller should not
// lose its place for changing the limit.
func TestTheLimitIsNotPartOfTheQuestion(t *testing.T) {
	s, _ := service(t, fullGrant())
	ctx := context.Background()
	req := &auditv1.SearchRequest{Profile: "security", Limit: 1}

	page, g, err := s.Search(ctx, caller(), req)
	if err != nil {
		t.Fatal(err)
	}
	cursors, err := s.Cursors(req, g, page)
	if err != nil {
		t.Fatal(err)
	}
	wider := &auditv1.SearchRequest{Profile: "security", Limit: 10, Cursor: cursors.GetNext()}
	if _, _, err := s.Search(ctx, caller(), wider); err != nil {
		t.Fatalf("changing the page size lost the place: %v", err)
	}
}

// A cursor that is not ours at all is refused as such.
func TestRubbishIsNotACursor(t *testing.T) {
	s, _ := service(t, fullGrant())
	_, _, err := s.Search(context.Background(), caller(),
		&auditv1.SearchRequest{Profile: "security", Cursor: "not-a-cursor"})
	if err == nil {
		t.Fatal("a made-up cursor was accepted")
	}
}
