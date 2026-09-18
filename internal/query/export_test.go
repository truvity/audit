package query_test

import (
	"context"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"errors"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/catalogue"
	auditv1 "github.com/truvity/audit/gen/audit/v1"

	"github.com/truvity/audit/internal/query"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store/storetest"
)

// links stands in for a store that can presign.
type links struct{ valid time.Duration }

func (l *links) Presign(_ context.Context, key string, valid time.Duration) (string, error) {
	l.valid = valid
	return "https://example.test/" + key, nil
}

func exporting(t *testing.T, g auth.Grant) (*query.Service, *storetest.Memory, *reads, *links) {
	t.Helper()
	common, err := catalogue.Common()
	if err != nil {
		t.Fatal(err)
	}
	into, files, presigner := &reads{}, storetest.NewMemory(), &links{}
	s, err := query.New(&query.Service{
		Searcher:   corpus(t),
		Authorizer: auth.Declarative{Rules: []auth.Rule{{Name: "a-rule", Grant: g}}},
		Sink:       into, Catalogue: common, Instance: "query-1",
		Exporter: &query.Exporter{
			Store: files, Presigner: presigner,
			Now: func() time.Time { return at(t, "2026-09-18T00:00:00Z") },
		},
		OnUnrecorded: func(action string, err error) {
			t.Errorf("%s was not recorded: %v", action, err)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, files, into, presigner
}

func exportGrant() auth.Grant {
	g := fullGrant()
	g.Operations = append(g.Operations, auth.Export)
	return g
}

// An export carries the rows the grant allows, and records both halves.
func TestExportWritesAndRecordsBothHalves(t *testing.T) {
	s, files, into, _ := exporting(t, exportGrant())
	ctx := context.Background()

	job, err := s.Export(ctx, caller(), &auditv1.ExportRequest{Profile: "security"})
	if err != nil {
		t.Fatal(err)
	}
	if job.Failed != "" {
		t.Fatalf("the export failed: %s", job.Failed)
	}
	if job.Records != 4 {
		t.Fatalf("%d records exported, want 4", job.Records)
	}
	if job.State() != auditv1.ExportState_EXPORT_STATE_READY {
		t.Fatalf("state %v", job.State())
	}

	// The file is outside every profile's prefix: an export must not be
	// mistaken for the archive.
	key := query.FileKey(job.ID, "ndjson")
	if !strings.HasPrefix(key, query.ExportPrefix+"/") {
		t.Fatalf("an export was written to %s", key)
	}
	body, err := files.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimRight(string(body), "\n"), "\n") + 1; lines != 4 {
		t.Fatalf("%d lines in the export, want 4", lines)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"audit.export.requested", "audit.export.completed"} {
		got := into.of(action)
		if len(got) != 1 {
			t.Fatalf("%d records of %s, want 1", len(got), action)
		}
	}
	// The completed record carries what the catalogue's schema declares.
	data := into.of("audit.export.completed")[0].GetData().AsMap()
	if data["records"] != float64(4) || data["format"] != "ndjson" {
		t.Fatalf("the completed record says %v", data)
	}
}

// The grant bounds an export as it bounds a search: it is the one read that
// leaves with the records, so it must not be the one that escapes the grant.
func TestAnExportIsBoundedByTheGrant(t *testing.T) {
	g := exportGrant()
	g.AllTenants, g.Tenants = false, []string{"acme"}
	s, _, _, _ := exporting(t, g)

	job, err := s.Export(context.Background(), caller(), &auditv1.ExportRequest{Profile: "security"})
	if err != nil {
		t.Fatal(err)
	}
	if job.Records != 2 {
		t.Fatalf("%d records exported, want the 2 of the granted tenant", job.Records)
	}
}

// Export is its own operation: a grant to search does not carry it.
func TestExportIsNotImpliedBySearch(t *testing.T) {
	s, _, into, _ := exporting(t, fullGrant())
	_, err := s.Export(context.Background(), caller(), &auditv1.ExportRequest{Profile: "security"})
	if err == nil {
		t.Fatal("a grant without export produced an export")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// And the attempt is recorded: an export is the read that leaves with the
	// records, so an attempt at one is worth knowing about.
	got := into.of("audit.export.requested")
	if len(got) != 1 {
		t.Fatalf("%d records of a refused export attempt", len(got))
	}
	if got[0].GetOutcome().GetResult() != auditv1.Outcome_RESULT_FAILURE {
		t.Fatal("a refused export was recorded as a success")
	}
}

// Whoever asked for it is whoever may collect it.
func TestOnlyTheAskerCollects(t *testing.T) {
	s, _, _, _ := exporting(t, exportGrant())
	ctx := context.Background()
	job, err := s.Export(ctx, caller(), &auditv1.ExportRequest{Profile: "security"})
	if err != nil {
		t.Fatal(err)
	}

	got, url, err := s.GetExport(ctx, caller(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if url == "" || got.Records != 4 {
		t.Fatalf("the asker could not collect: %q %+v", url, got)
	}

	other := auth.Principal{Issuer: "https://issuer.test", Subject: "someone-else", Via: "jwt"}
	if _, _, err := s.GetExport(ctx, other, job.ID); err == nil {
		t.Fatal("a second person collected an export on the strength of its identifier")
	}
}

// An export is an unlocked copy of audit records, so it expires, and so does
// the link.
func TestAnExportAndItsLinkExpire(t *testing.T) {
	s, files, _, presigner := exporting(t, exportGrant())
	ctx := context.Background()
	job, err := s.Export(ctx, caller(), &auditv1.ExportRequest{Profile: "security"})
	if err != nil {
		t.Fatal(err)
	}
	if !job.ExpiresAt.After(job.AskedAt) {
		t.Fatal("an export with no expiry is a second archive nobody is managing")
	}
	if _, _, err := s.GetExport(ctx, caller(), job.ID); err != nil {
		t.Fatal(err)
	}
	if presigner.valid <= 0 || presigner.valid > 24*time.Hour {
		t.Fatalf("the link is valid for %v, which is not short", presigner.valid)
	}

	// The file carries its expiry as metadata and NOT as a retention: a
	// retention would keep it, and the point is that it goes. The bucket's
	// lifecycle clears the export prefix.
	entry, err := files.Head(ctx, query.FileKey(job.ID, "ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if !entry.RetainUntil.IsZero() {
		t.Fatalf("an export was written with a retention until %s, which would keep it", entry.RetainUntil)
	}
	obj, _ := files.Object(query.FileKey(job.ID, "ndjson"))
	if obj.Metadata["audit-expires"] != job.ExpiresAt.Format(time.RFC3339) {
		t.Fatalf("the file does not say when it expires: %v", obj.Metadata)
	}
}

// An export is bounded. A grant that happens to allow a whole profile must not
// turn one request into reading the profile into memory.
func TestAnExportOverTheCapFailsAndSaysSo(t *testing.T) {
	s, _, _, _ := exporting(t, exportGrant())
	s.Exporter.MaxRecords = 2
	job, err := s.Export(context.Background(), caller(), &auditv1.ExportRequest{Profile: "security"})
	if err != nil {
		t.Fatal(err)
	}
	if job.State() != auditv1.ExportState_EXPORT_STATE_ERROR {
		t.Fatalf("an export over the cap was %v", job.State())
	}
	if !strings.Contains(job.Failed, "narrow the filter") {
		t.Fatalf("the failure should say what to do: %s", job.Failed)
	}
}

// If the trail cannot say an export was asked for, it does not happen. This is
// the one place in the service where recording is allowed to stop the read.
func TestAnExportThatCannotBeRecordedDoesNotHappen(t *testing.T) {
	common, err := catalogue.Common()
	if err != nil {
		t.Fatal(err)
	}
	files := storetest.NewMemory()
	refusing := sink.Func(func(context.Context, *sink.Request) (*sink.Result, error) {
		return nil, errors.New("the writer is unreachable")
	})
	s, err := query.New(&query.Service{
		Searcher: corpus(t), Catalogue: common, Sink: refusing,
		Authorizer: auth.Declarative{Rules: []auth.Rule{{Name: "r", Grant: exportGrant()}}},
		Exporter:   &query.Exporter{Store: files, Presigner: &links{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Export(context.Background(), caller(), &auditv1.ExportRequest{Profile: "security"})
	if err == nil {
		t.Fatal("an export ran with its request unrecorded")
	}
	if !strings.Contains(err.Error(), "could not be recorded") {
		t.Fatalf("the error should say why: %v", err)
	}
	if files.Len() != 0 {
		t.Fatalf("%d objects were written for an export that must not have started", files.Len())
	}
}

// Without somewhere to put an export, or a way to collect one, the service
// refuses rather than filling a bucket with files nobody can reach.
func TestExportRefusesWithoutItsParts(t *testing.T) {
	common, err := catalogue.Common()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		with *query.Exporter
		want string
	}{
		{"no exporter", nil, "no exporter"},
		{"no presigner", &query.Exporter{Store: storetest.NewMemory()}, "never collected"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, err := query.New(&query.Service{
				Searcher: corpus(t), Catalogue: common,
				Authorizer: auth.Declarative{Rules: []auth.Rule{{Name: "r", Grant: exportGrant()}}},
				Exporter:   c.with,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Export(context.Background(), caller(),
				&auditv1.ExportRequest{Profile: "security"})
			if err == nil {
				t.Fatal("want a refusal")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the refusal should say %q: %v", c.want, err)
			}
		})
	}
}

// CSV is a header and a row per record, for somebody opening it in a
// spreadsheet rather than a program.
func TestCSVExportHasAHeaderAndARowEach(t *testing.T) {
	s, files, _, _ := exporting(t, exportGrant())
	ctx := context.Background()
	job, err := s.Export(ctx, caller(), &auditv1.ExportRequest{
		Profile: "security", Format: auditv1.ExportRequest_FORMAT_CSV,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := files.Get(ctx, query.FileKey(job.ID, "csv"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("%d csv rows, want a header and 4 records", len(rows))
	}
	if rows[0][0] != "id" {
		t.Fatalf("the first row is not a header: %v", rows[0])
	}
}
