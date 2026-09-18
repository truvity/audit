package s3scan_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/index/s3scan"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/storetest"
)

func at(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.UTC()
}

var fields = index.Fields{
	Filter: []string{"/credential_type"},
	Facet:  []string{"/credential_type"},
}

func fieldsOf(context.Context, *record.Record) (index.Fields, error) { return fields, nil }

// archived writes one object as the writer would, with the records given.
func archived(t *testing.T, s *storetest.Memory, tenant string, day time.Time, name string, rows ...*record.Record) string {
	t.Helper()
	var lines []byte
	for _, r := range rows {
		line, err := record.Canonical(r)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(append(lines, line...), '\n')
	}
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	body := encoder.EncodeAll(lines, nil)
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	key := "profile=security/tenant=" + tenant + "/year=" + day.Format("2006") +
		"/month=" + day.Format("01") + "/day=" + day.Format("02") + "/" + name
	if err := s.Put(context.Background(), store.Object{
		Key: key, Body: body, RetainUntil: day.AddDate(1, 0, 0),
	}); err != nil {
		t.Fatal(err)
	}
	return key
}

func made(t *testing.T, id string, occurred time.Time, action, outcome, tenant string) *record.Record {
	t.Helper()
	data, err := structpb.NewStruct(map[string]any{"credential_type": "pid"})
	if err != nil {
		t.Fatal(err)
	}
	r := &record.Record{
		Id: id, SchemaVersion: record.SchemaVersion, CatalogueVersion: "1.0.0",
		Source: "wallet", TenantId: tenant, Action: action,
		Operation: auditv1.Operation_OPERATION_CREATE,
		Outcome:   &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS, Reason: outcome},
		Actor:     &record.Actor{Kind: "operator", Id: "olga"},
		Targets:   []*record.Target{{Type: "credential", Id: "cred-1"}},
		Data:      data,
	}
	record.Assign(r)
	r.OccurredAt = timestamppb.New(occurred)
	r.RecordedAt = timestamppb.New(occurred)
	if outcome == "failure" {
		r.Outcome = &record.Outcome{Result: auditv1.Outcome_RESULT_FAILURE}
	}
	return r
}

func id(n int) string {
	return "018f0000-0000-7000-8000-00000000000" + string("0123456789abcdef"[n])
}

// scanned sets up an archive of three days and a scanner over it.
func scanned(t *testing.T) (*s3scan.Scanner, *storetest.Memory) {
	t.Helper()
	s := storetest.NewMemory()
	base := at(t, "2026-09-15T10:00:00Z")
	n := 0
	for d := 0; d < 3; d++ {
		day := base.AddDate(0, 0, d)
		var rows []*record.Record
		for i := 0; i < 2; i++ {
			action := "wallet.credential.issued"
			outcome := "success"
			if n%3 == 2 {
				outcome = "failure"
			}
			if n%3 == 1 {
				action = "wallet.credential.revoked"
			}
			// Tenants alternate by day, so that a key from a later day sorts
			// BEFORE a key from an earlier day: the walk is by day, not by
			// key, and a cursor that compared keys alone would skip the
			// earlier day's other tenant.
			tenant := "acme"
			if d == 1 {
				tenant = "globex"
			}
			rows = append(rows, made(t, id(n), day.Add(time.Duration(i)*time.Minute), action, outcome, tenant))
			n++
		}
		archived(t, s, rows[0].GetTenantId(), day, "a.ndjson.zst", rows...)
	}
	return &s3scan.Scanner{
		Store: s, Fields: fieldsOf,
		Now: func() time.Time { return at(t, "2026-09-18T00:00:00Z") },
	}, s
}

func ids(page index.Page) []string {
	out := make([]string, 0, len(page.Rows))
	for _, r := range page.Rows {
		out = append(out, r.ID)
	}
	return out
}

// A searcher with no index still answers the same closed query.
func TestScanFindsWhatMatches(t *testing.T) {
	scanner, _ := scanned(t)
	page, err := scanner.Search(context.Background(), index.Query{
		Profile: "security", Limit: 100,
		Filter: []index.Conjunction{{
			Action: []index.Predicate{{Op: index.Equal, Value: "wallet.credential.issued"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 4 {
		t.Fatalf("%d rows, want 4: %v", len(page.Rows), ids(page))
	}
	for _, r := range page.Rows {
		if r.Action != "wallet.credential.issued" {
			t.Fatalf("a row that does not match came back: %+v", r.Action)
		}
		if r.ObjectKey == "" || r.Line == 0 {
			t.Fatalf("a scanned row has no provenance: %+v", r)
		}
	}
}

// The grant is a term here too, and a searcher that ignored it would be the
// hole in the design that every other one is careful about.
func TestScanHonoursTheGrant(t *testing.T) {
	scanner, _ := scanned(t)
	page, err := scanner.Search(context.Background(), index.Query{
		Profile: "security", Tenants: []string{"acme"}, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) == 0 {
		t.Fatal("nothing came back")
	}
	for _, r := range page.Rows {
		if r.TenantID != "acme" {
			t.Fatalf("a row of %s came back under a grant for acme", r.TenantID)
		}
	}
}

// Paging without an index resumes from a place in the archive.
func TestScanPagesWithoutRepeatingOrSkipping(t *testing.T) {
	scanner, _ := scanned(t)
	ctx := context.Background()
	q := index.Query{Profile: "security", Limit: 2}

	var seen []string
	for page := 0; page < 6; page++ {
		got, err := scanner.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, ids(got)...)
		if !got.More {
			break
		}
		q.After = got.Next
	}
	if len(seen) != 6 {
		t.Fatalf("paged %d rows of 6: %v", len(seen), seen)
	}
	unique := map[string]bool{}
	for _, s := range seen {
		if unique[s] {
			t.Fatalf("a row came back on two pages: %v", seen)
		}
		unique[s] = true
	}
}

// A scan is bounded. Out of budget it returns what it found with a cursor,
// rather than running until something times out and leaving nothing.
func TestScanStopsWhenTheBudgetIsSpent(t *testing.T) {
	scanner, _ := scanned(t)
	scanner.Budget = s3scan.Budget{Objects: 1}

	page, err := scanner.Search(context.Background(), index.Query{Profile: "security", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !page.More {
		t.Fatal("a scan that spent its budget did not say there was more")
	}
	if page.Next == nil {
		t.Fatal("a scan that stopped early gave no way to resume")
	}
	if len(page.Rows) == 0 {
		t.Fatal("the budget bought nothing")
	}
}

// Without a range it walks back to a horizon rather than to the beginning of
// the archive: a seven-year retention is not an answer anybody is waiting for.
func TestScanStopsAtTheHorizon(t *testing.T) {
	scanner, s := scanned(t)
	old := at(t, "2020-01-01T10:00:00Z")
	archived(t, s, "acme", old, "old.ndjson.zst",
		made(t, id(15), old, "wallet.credential.issued", "success", "acme"))

	page, err := scanner.Search(context.Background(), index.Query{Profile: "security", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range page.Rows {
		if r.ID == id(15) {
			t.Fatal("a record older than the horizon was read without being asked for")
		}
	}

	// Asked for by name, the range reaches it.
	page, err = scanner.Search(context.Background(), index.Query{
		Profile: "security", Limit: 100,
		Filter: []index.Conjunction{{
			OccurredAt: []index.TimePredicate{{From: at(t, "2019-12-31T00:00:00Z")}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range page.Rows {
		if r.ID == id(15) {
			found = true
		}
	}
	if !found {
		t.Fatal("a range that names the day did not reach it")
	}
}

// What it cannot do it refuses, rather than quietly answering something
// narrower than was asked.
func TestScanRefusesWhatItCannotDo(t *testing.T) {
	scanner, _ := scanned(t)
	ctx := context.Background()

	if _, err := scanner.Facets(ctx, index.Query{Profile: "security"},
		[]string{index.FieldAction}, 10); err == nil {
		t.Fatal("a searcher with no index offered to count values")
	}
	if _, err := scanner.Search(ctx, index.Query{
		Profile: "security", Sort: []index.SortBy{{Field: index.SortAction}},
	}); err == nil {
		t.Fatal("an ordering the archive cannot deliver was accepted")
	}
	if !scanner.Capabilities().DataPredicates {
		t.Fatal("a scan holds the whole record and should filter on its properties")
	}
	if scanner.Capabilities().Facets {
		t.Fatal("capabilities claim facets this searcher refuses")
	}
}

// A cursor belongs to the searcher that issued it as much as to the query.
func TestScanRefusesACursorFromElsewhere(t *testing.T) {
	scanner, _ := scanned(t)
	_, err := scanner.Search(context.Background(), index.Query{
		Profile: "security",
		After:   &index.Boundary{Values: []string{"2026-09-17T10:00:00Z"}, ID: id(1)},
	})
	if err == nil {
		t.Fatal("a cursor from an indexed searcher was accepted")
	}
	if !strings.Contains(err.Error(), "did not come from this searcher") {
		t.Errorf("the refusal should say why: %v", err)
	}
}

// Get has no index to look in, so it says so rather than reporting absence.
func TestScanGetFindsAndSaysWhenItCannot(t *testing.T) {
	scanner, _ := scanned(t)
	ctx := context.Background()
	got, where, err := scanner.Get(ctx, "security", id(0))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id(0) || where.ObjectKey == "" {
		t.Fatalf("get: %+v %+v", got, where)
	}
	if _, _, err := scanner.Get(ctx, "security", id(9)); err == nil {
		t.Fatal("a record that is not there was found")
	}
}

// The walk is newest day first and the tenant sits BEFORE the date in a key, so
// a key from an earlier day under a later-sorting tenant is lexicographically
// after the cursor's. A cursor that compared keys alone would skip that whole
// tenant's day. Paged one row at a time, every row must still come back once.
func TestScanCursorFollowsTheWalkNotTheKeyOrder(t *testing.T) {
	scanner, _ := scanned(t)
	ctx := context.Background()
	q := index.Query{Profile: "security", Limit: 1}
	var seen []string
	for page := 0; page < 10; page++ {
		got, err := scanner.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, ids(got)...)
		if !got.More {
			break
		}
		q.After = got.Next
	}
	if len(seen) != 6 {
		t.Fatalf("paged %d rows of 6 one at a time: %v", len(seen), seen)
	}
}
