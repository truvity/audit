package cli_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/storetest"
)

// object writes an archive object of the shape the writer writes.
func object(t *testing.T, s *storetest.Memory, profile, tenant string, day time.Time, records ...*record.Record) string {
	t.Helper()
	var lines []byte
	for _, r := range records {
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

	key := fmt.Sprintf("profile=%s/tenant=%s/year=%s/month=%s/day=%s/%s.ndjson.zst",
		profile, tenant, day.Format("2006"), day.Format("01"), day.Format("02"),
		day.Format("150405")+"-"+records[0].GetId())
	if err := s.Put(context.Background(), store.Object{
		Key: key, Body: body, ContentType: "application/x-ndjson", Encoding: "zstd",
	}); err != nil {
		t.Fatal(err)
	}
	return key
}

func indexed(t *testing.T, id string, recorded time.Time) *record.Record {
	t.Helper()
	data, err := structpb.NewStruct(map[string]any{"credential_type": "pid"})
	if err != nil {
		t.Fatal(err)
	}
	r := &record.Record{
		Id:               id,
		SchemaVersion:    record.SchemaVersion,
		CatalogueVersion: "1.0.0",
		Source:           "wallet",
		Action:           "wallet.credential.issued",
		TenantId:         "acme",
		Data:             data,
	}
	record.Assign(r)
	r.RecordedAt = timestamppb.New(recorded)
	r.OccurredAt = timestamppb.New(recorded.Add(-time.Minute))
	return r
}

var catalogueFields = func(context.Context, *record.Record) (index.Fields, error) {
	return index.Fields{Filter: []string{"/credential_type"}, Facet: []string{"/credential_type"}}, nil
}

// This is the promise the index rests on: everything a search answers with can
// be derived again from the objects, which are the ones under an object lock
// that a signed digest chain accounts for.
func TestReindexRebuildsFromTheArchive(t *testing.T) {
	s := storetest.NewMemory()
	day := at(t, "2026-09-17T10:17:00Z")
	key := object(t, s, "security", "acme",
		day, indexed(t, "018f0000-0000-7000-8000-00000000000a", day),
		indexed(t, "018f0000-0000-7000-8000-00000000000b", day))

	target := index.NewMemory()
	report, err := cli.Reindex{
		Store: s, Index: target, Fields: catalogueFields,
		Profile: "security", From: day, To: day,
		Out: &strings.Builder{},
	}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Objects != 1 || report.Records != 2 {
		t.Fatalf("read %d objects and %d records, want 1 and 2", report.Objects, report.Records)
	}
	if len(report.Unreadable) != 0 {
		t.Fatalf("unreadable: %v", report.Unreadable)
	}

	rows := target.Rows("security")
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	// A row has to point back at the line it came from, or an answer cannot be
	// checked against the copy the digest chain covers.
	if rows[0].ObjectKey != key || rows[0].Line != 1 || rows[1].Line != 2 {
		t.Fatalf("the rows do not address their lines: %+v", rows)
	}
	if len(rows[0].Data) != 1 || rows[0].Data[0].Text != "pid" {
		t.Fatalf("the catalogue's indexed properties were not rebuilt: %+v", rows[0].Data)
	}
}

// A reindex over a range that is already indexed is the ordinary case: an
// operator repairing an afternoon does not know exactly where the gap starts.
func TestReindexTwiceCountsOnce(t *testing.T) {
	s := storetest.NewMemory()
	day := at(t, "2026-09-17T10:17:00Z")
	object(t, s, "security", "acme", day, indexed(t, "018f0000-0000-7000-8000-00000000000a", day))

	target := index.NewMemory()
	run := cli.Reindex{
		Store: s, Index: target, Fields: catalogueFields,
		Profile: "security", From: day, To: day, Out: &strings.Builder{},
	}
	for n := 0; n < 2; n++ {
		if _, err := run.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(target.Rows("security")); got != 1 {
		t.Fatalf("%d rows after two passes, want 1", got)
	}
	hour := day.Truncate(time.Hour)
	if got := target.Count("security", "acme", index.FieldAction, "wallet.credential.issued", hour); got != 1 {
		t.Fatalf("counted %d after two passes, want 1", got)
	}
}

// Only the range asked for, and only the profile asked for. An operator
// repairing one afternoon must not pay for the whole archive.
func TestReindexTakesOnlyTheRangeAsked(t *testing.T) {
	s := storetest.NewMemory()
	wanted := at(t, "2026-09-17T10:17:00Z")
	other := at(t, "2026-09-18T10:17:00Z")
	object(t, s, "security", "acme", wanted, indexed(t, "018f0000-0000-7000-8000-00000000000a", wanted))
	object(t, s, "security", "acme", other, indexed(t, "018f0000-0000-7000-8000-00000000000b", other))
	object(t, s, "history", "acme", wanted, indexed(t, "018f0000-0000-7000-8000-00000000000c", wanted))

	target := index.NewMemory()
	report, err := cli.Reindex{
		Store: s, Index: target, Fields: catalogueFields,
		Profile: "security", From: wanted, To: wanted, Out: &strings.Builder{},
	}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Records != 1 {
		t.Fatalf("%d records, want only the day and profile asked for", report.Records)
	}
	if target.Len("history") != 0 {
		t.Fatal("another profile was reindexed")
	}
}

// An object the archive holds and nothing can read is a finding, not a detail
// to pass over in silence.
func TestReindexReportsWhatItCouldNotRead(t *testing.T) {
	s := storetest.NewMemory()
	day := at(t, "2026-09-17T10:17:00Z")
	key := object(t, s, "security", "acme", day, indexed(t, "018f0000-0000-7000-8000-00000000000a", day))
	s.Replace(key, []byte("this is not zstd"))

	report, err := cli.Reindex{
		Store: s, Index: index.NewMemory(), Fields: catalogueFields,
		Profile: "security", From: day, To: day, Out: &strings.Builder{},
	}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Unreadable) != 1 || report.Unreadable[0] != key {
		t.Fatalf("unreadable: %v, want the corrupt object named", report.Unreadable)
	}
}

// Rebuilding without the catalogue would produce an index missing its data
// columns, and because indexing counts a record once, a later run with the
// catalogue could not repair it. Refusing is the only honest answer.
func TestReindexRefusesWithoutACatalogue(t *testing.T) {
	day := at(t, "2026-09-17T10:17:00Z")
	_, err := cli.Reindex{
		Store: storetest.NewMemory(), Index: index.NewMemory(),
		Profile: "security", From: day, To: day, Out: &strings.Builder{},
	}.Run(context.Background())
	if err == nil {
		t.Fatal("a reindex without a catalogue must be refused")
	}
	if !strings.Contains(err.Error(), "catalogue") {
		t.Errorf("the refusal should say what is missing: %v", err)
	}
}
