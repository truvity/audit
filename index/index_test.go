package index_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/record"
)

func at(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.UTC()
}

func copied(t *testing.T, id string) *record.Record {
	t.Helper()
	data, err := structpb.NewStruct(map[string]any{
		"credential_type": "pid",
		"attempt":         float64(3),
		"expires_at":      "2027-01-01T00:00:00Z",
		"revoked":         true,
		"note":            "not indexed",
		"nested":          map[string]any{"deep": "value"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &record.Record{
		Id:            id,
		TenantId:      "acme",
		OccurredAt:    timestamppb.New(at(t, "2026-09-17T10:15:00Z")),
		RecordedAt:    timestamppb.New(at(t, "2026-09-17T10:17:30Z")),
		Sequence:      7,
		Source:        "wallet",
		Action:        "wallet.credential.issued",
		Operation:     auditv1.Operation_OPERATION_CREATE,
		Outcome:       &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
		Actor:         &record.Actor{Kind: "operator", Id: "ps_abc"},
		Subject:       &record.Party{Kind: "person", Id: "ps_def"},
		Observer:      &record.Observer{Id: "workload:wallet", Instance: "wallet-7"},
		Context:       &record.Context{ClientAddresses: []string{"203.0.113.9", "198.51.100.4"}, RequestId: "req-1", TraceId: "trace-1"},
		Targets:       []*record.Target{{Type: "credential", Id: "cred-1"}, {Type: "credential", Id: "cred-2"}},
		Data:          data,
		SchemaVersion: record.SchemaVersion,
	}
}

var fields = index.Fields{
	Filter: []string{"/credential_type", "/attempt", "/expires_at", "/revoked", "/nested/deep"},
	Facet:  []string{"/credential_type"},
}

// The row is what a reader needs to find the copy again and check the answer
// against the object the digest chain accounts for.
func TestRowOfCarriesWhereTheCopyIs(t *testing.T) {
	row := index.RowOf(copied(t, "a"), index.ObjectAt{Key: "profile=security/x.ndjson.zst", Line: 4}, fields)
	if row.ObjectKey == "" || row.Line != 4 {
		t.Fatalf("the row does not say where the copy is: %+v", row)
	}
	if row.Action != "wallet.credential.issued" || row.Operation != "create" || row.Outcome != "success" {
		t.Fatalf("operation and outcome are not in their short spelling: %+v", row)
	}
	if len(row.TargetTypes) != 2 || row.TargetIDs[1] != "cred-2" {
		t.Fatalf("targets: %+v", row)
	}
}

// The nearest address is the one a query means by "where from", and the chain
// arrives nearest last.
func TestRowOfTakesTheNearestAddress(t *testing.T) {
	row := index.RowOf(copied(t, "a"), index.ObjectAt{}, fields)
	if row.ClientAddress != "198.51.100.4" {
		t.Fatalf("client address = %q, want the nearest hop", row.ClientAddress)
	}
}

// A property the catalogue did not mark is not indexed. Indexing everything
// would put the extension schema's decisions back in the hands of whoever
// writes the query.
func TestOnlyMarkedPropertiesAreIndexed(t *testing.T) {
	row := index.RowOf(copied(t, "a"), index.ObjectAt{}, fields)
	byPath := map[string]index.Value{}
	for _, v := range row.Data {
		byPath[v.Path] = v
	}
	if _, indexed := byPath["/note"]; indexed {
		t.Fatal("an unmarked property was indexed")
	}
	if got := byPath["/credential_type"]; got.Kind != index.Text || got.Text != "pid" {
		t.Fatalf("/credential_type: %+v", got)
	}
	if got := byPath["/attempt"]; got.Kind != index.Int || got.Int != 3 {
		t.Fatalf("/attempt: %+v", got)
	}
	if got := byPath["/expires_at"]; got.Kind != index.Time {
		t.Fatalf("/expires_at: %+v", got)
	}
	if got := byPath["/revoked"]; got.Kind != index.Bool || got.String() != "true" {
		t.Fatalf("/revoked: %+v", got)
	}
	if got := byPath["/nested/deep"]; got.Text != "value" {
		t.Fatalf("a nested pointer was not followed: %+v", got)
	}
}

// A record naming one target type twice counts once: the facet answers how many
// records touched it, not how many rows a record had.
func TestFacetsCountARecordOnce(t *testing.T) {
	row := index.RowOf(copied(t, "a"), index.ObjectAt{}, fields)
	n := 0
	for _, d := range row.Facets() {
		if d.Field == index.FieldTargetType && d.Value == "credential" {
			n += int(d.Count)
		}
	}
	if n != 1 {
		t.Fatalf("one record counted %d times against one target type", n)
	}
}

// Facets fall in the hour the record was recorded in, not the hour it happened
// in: a late record must land in a window that is still open to counting.
func TestFacetsUseTheRecordedHour(t *testing.T) {
	for _, d := range index.RowOf(copied(t, "a"), index.ObjectAt{}, fields).Facets() {
		if !d.Hour.Equal(at(t, "2026-09-17T10:00:00Z")) {
			t.Fatalf("%s is not in the recorded hour", d)
		}
	}
}

// A property marked as a facet is counted under its own name, which is what
// lets an application's own data become a facet without any code.
func TestADataFacetIsCountedUnderItsPointer(t *testing.T) {
	var found bool
	for _, d := range index.RowOf(copied(t, "a"), index.ObjectAt{}, fields).Facets() {
		if d.Field == index.DataField+"/credential_type" && d.Value == "pid" {
			found = true
		}
		if d.Field == index.DataField+"/attempt" {
			t.Fatal("a filterable property that is not a facet was counted")
		}
	}
	if !found {
		t.Fatal("a data facet was not counted")
	}
}

// Every hop below the writer is at-least-once. The index is where that stops
// mattering, and a count that moved on a repeat would be the one thing no
// caller could correct.
func TestIndexingTheSameCopyTwiceCountsItOnce(t *testing.T) {
	m := index.NewMemory()
	row := index.RowOf(copied(t, "a"), index.ObjectAt{Key: "k", Line: 1}, fields)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := m.Index(ctx, "security", []index.Row{row}); err != nil {
			t.Fatal(err)
		}
	}
	if m.Len("security") != 1 {
		t.Fatalf("%d rows, want 1", m.Len("security"))
	}
	hour := at(t, "2026-09-17T10:00:00Z")
	if got := m.Count("security", "acme", index.FieldAction, "wallet.credential.issued", hour); got != 1 {
		t.Fatalf("counted %d, want 1", got)
	}
}

// Two copies in one batch are counted together, so the counts table is touched
// once per value rather than once per record.
func TestMergeAddsUpOneBatch(t *testing.T) {
	m := index.NewMemory()
	rows := []index.Row{
		index.RowOf(copied(t, "a"), index.ObjectAt{Key: "k", Line: 1}, fields),
		index.RowOf(copied(t, "b"), index.ObjectAt{Key: "k", Line: 2}, fields),
	}
	if err := m.Index(context.Background(), "security", rows); err != nil {
		t.Fatal(err)
	}
	hour := at(t, "2026-09-17T10:00:00Z")
	if got := m.Count("security", "acme", index.FieldAction, "wallet.credential.issued", hour); got != 2 {
		t.Fatalf("counted %d, want 2", got)
	}
}

// A profile keeps who did it for less time than it keeps what was done. The
// index follows, or it would answer a question the archive no longer can.
func TestPurgeIdentifyingKeepsTheEvent(t *testing.T) {
	m := index.NewMemory()
	row := index.RowOf(copied(t, "a"), index.ObjectAt{Key: "k", Line: 1}, fields)
	ctx := context.Background()
	if err := m.Index(ctx, "security", []index.Row{row}); err != nil {
		t.Fatal(err)
	}
	if err := m.Purge(ctx, "security", at(t, "2026-10-01T00:00:00Z"), index.Identifying); err != nil {
		t.Fatal(err)
	}
	got, ok := m.Row("security", "a")
	if !ok {
		t.Fatal("purging the identifying columns removed the event")
	}
	if got.ActorID != "" || got.SubjectID != "" || got.ClientAddress != "" {
		t.Fatalf("identifying columns survived: %+v", got)
	}
	if got.Action == "" || got.ActorKind == "" {
		t.Fatal("what happened, and what kind of actor did it, must survive")
	}

	if err := m.Purge(ctx, "security", at(t, "2026-10-01T00:00:00Z"), index.Everything); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Row("security", "a"); ok {
		t.Fatal("a row past its profile's retention was kept")
	}
}

// A tail advances on recorded order, so that a poller cannot miss a late
// record that arrived after one it has already seen.
func TestRowsAreInRecordedOrder(t *testing.T) {
	m := index.NewMemory()
	first := index.RowOf(copied(t, "a"), index.ObjectAt{Key: "k", Line: 1}, fields)
	second := index.RowOf(copied(t, "b"), index.ObjectAt{Key: "k", Line: 2}, fields)
	second.RecordedAt = second.RecordedAt.Add(time.Minute)
	if err := m.Index(context.Background(), "security", []index.Row{second, first}); err != nil {
		t.Fatal(err)
	}
	rows := m.Rows("security")
	if len(rows) != 2 || rows[0].ID != "a" {
		t.Fatalf("rows out of recorded order: %+v", rows)
	}
}
