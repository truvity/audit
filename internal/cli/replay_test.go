package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
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

// deadLetter writes an envelope of the shape the writer writes.
func deadLetter(t *testing.T, s *storetest.Memory, day time.Time, seq int, reason, action string) {
	t.Helper()
	r := &record.Record{
		Id:               record.NewID(),
		SchemaVersion:    record.SchemaVersion,
		CatalogueVersion: "1.0.0",
		Source:           "audit",
		Action:           action,
		TenantId:         record.TenantPlatform,
	}
	record.Assign(r)
	line, err := record.Canonical(r)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"reason": reason, "at": day.Format(time.RFC3339Nano), "writer": "writer-1",
		"id": r.GetId(), "source": r.GetSource(), "action": action,
		"record": json.RawMessage(line),
	})
	if err != nil {
		t.Fatal(err)
	}
	key := day.Format("dlq/year=2006/month=01/day=02/") +
		day.Format("20060102150405") + "-writer-1-" + string(rune('0'+seq)) + ".json"
	if err := s.Put(context.Background(), store.Object{
		Key: key, Body: body, RetainUntil: day.AddDate(10, 0, 0),
	}); err != nil {
		t.Fatal(err)
	}
}

// A replay hands the records back with their identifiers intact, which is what
// lets the writer's deduplication make a replay of something that did get
// through cost nothing.
func TestReplaySendsTheRecordsBack(t *testing.T) {
	s := storetest.NewMemory()
	day := at(t, "2026-09-17T10:00:00Z")
	deadLetter(t, s, day, 1, "no catalogue wallet version 9.9.9 is registered", "wallet.credential.issued")
	deadLetter(t, s, day, 2, "no catalogue wallet version 9.9.9 is registered", "wallet.credential.issued")

	got := &sink.Memory{}
	var out bytes.Buffer
	report, err := cli.Replay{
		Store: s, Sink: got, From: day, To: day, Out: &out,
	}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Read != 2 || report.Sent != 2 {
		t.Fatalf("read %d sent %d, want 2 and 2", report.Read, report.Sent)
	}
	if n := len(got.Records()); n != 2 {
		t.Fatalf("the writer received %d records", n)
	}
	for _, r := range got.Records() {
		if r.GetId() == "" {
			t.Fatal("a replayed record lost its identifier, so deduplication could not stop a repeat")
		}
	}
}

// An operator fixes one cause at a time and replays that one, not the rest.
func TestReplayFiltersByReasonAndAction(t *testing.T) {
	s := storetest.NewMemory()
	day := at(t, "2026-09-17T10:00:00Z")
	deadLetter(t, s, day, 1, "no catalogue wallet version 9.9.9 is registered", "wallet.credential.issued")
	deadLetter(t, s, day, 2, "no configured profile keeps this action", "wallet.credential.revoked")

	got := &sink.Memory{}
	var out bytes.Buffer
	report, err := cli.Replay{
		Store: s, Sink: got, From: day, To: day,
		Reason: "no configured profile", Out: &out,
	}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Read != 2 || report.Sent != 1 || report.Skipped != 1 {
		t.Fatalf("read %d sent %d skipped %d", report.Read, report.Sent, report.Skipped)
	}
	if got.Records()[0].GetAction() != "wallet.credential.revoked" {
		t.Fatalf("the wrong record was replayed: %s", got.Records()[0].GetAction())
	}
}

// Without a sink nothing is sent, and the summary is what an operator reads to
// decide which cause to replay.
func TestReplayDryRunSendsNothingAndGroupsTheReasons(t *testing.T) {
	s := storetest.NewMemory()
	day := at(t, "2026-09-17T10:00:00Z")
	deadLetter(t, s, day, 1, "no catalogue wallet version 9.9.9 is registered", "wallet.credential.issued")
	deadLetter(t, s, day, 2, "no catalogue wallet version 9.9.9 is registered", "wallet.credential.issued")
	deadLetter(t, s, day, 3, "no configured profile keeps this action", "wallet.credential.revoked")

	var out bytes.Buffer
	report, err := cli.Replay{Store: s, From: day, To: day, Out: &out}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Sent != 0 {
		t.Fatalf("a dry run sent %d records", report.Sent)
	}
	if report.Reasons["no catalogue wallet version 9.9.9 is registered"] != 2 {
		t.Fatalf("reasons were not grouped: %v", report.Reasons)
	}
	if !strings.Contains(out.String(), "dry run") {
		t.Fatalf("the output does not say it sent nothing:\n%s", out.String())
	}
}

// A record that fails again lands back under the dead-letter prefix. The writer
// accepts it, because a record that can never be valid must not be retried
// forever by everything below, so counting what is newly there is the only
// honest way to report it.
func TestReplayReportsWhatFailedAgain(t *testing.T) {
	s := storetest.NewMemory()
	day := at(t, "2026-09-17T10:00:00Z")
	deadLetter(t, s, day, 1, "no catalogue wallet version 9.9.9 is registered", "wallet.credential.issued")

	// A sink that dead-letters what it is given, as a writer would.
	failing := sink.Func(func(_ context.Context, req *sink.Request) (*sink.Result, error) {
		for i, r := range req.Records {
			deadLetter(t, s, day.Add(time.Hour), 5+i, "still no catalogue", r.GetAction())
		}
		return &sink.Result{Accepted: len(req.Records)}, nil
	})

	var out bytes.Buffer
	report, err := cli.Replay{Store: s, Sink: failing, From: day, To: day, Out: &out}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.DeadEnd != 1 {
		t.Fatalf("failed again: %d, want 1", report.DeadEnd)
	}
	if !strings.Contains(out.String(), "failed") {
		t.Fatalf("the output does not report the failure:\n%s", out.String())
	}
}

// The range is read day by day, so a replay of one day does not take another's.
func TestReplayKeepsToItsRange(t *testing.T) {
	s := storetest.NewMemory()
	day := at(t, "2026-09-17T10:00:00Z")
	deadLetter(t, s, day, 1, "a reason", "wallet.credential.issued")
	deadLetter(t, s, day.AddDate(0, 0, 2), 1, "a reason", "wallet.credential.issued")

	var out bytes.Buffer
	report, err := cli.Replay{Store: s, From: day, To: day, Out: &out}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Read != 1 {
		t.Fatalf("read %d, want only the day asked for", report.Read)
	}
}
