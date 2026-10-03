package cli_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/recobj"
	"github.com/truvity/audit/internal/ulid"
	"github.com/truvity/audit/sdk/catalogue"
	auditv1 "github.com/truvity/audit/sdk/gen/audit/v1"
	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/storetest"
)

// events returns the records of one action, in order.
func events(into *collector, action string) []*record.Record {
	var out []*record.Record
	for _, r := range into.records {
		if r.GetAction() == action {
			out = append(out, r)
		}
	}
	return out
}

func common(t *testing.T) *catalogue.Catalogue {
	t.Helper()
	c, err := catalogue.Common()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// copyOf is a record as the profile "security" keeps it.
func copyOf(t *testing.T, id string, at time.Time) *record.Record {
	t.Helper()
	r := indexed(t, id, at)
	r.Profile = "security"
	return r
}

func verifier(t *testing.T, s *storetest.Memory, from, to time.Time) (cli.Verify, *collector) {
	t.Helper()
	into := &collector{}
	return cli.Verify{
		Store: s, Profile: "security", From: from, To: to,
		Sink: into, Catalogue: common(t), Instance: "verify-1", Out: &strings.Builder{},
	}, into
}

// A clean archive verifies, and every hour that had objects is recorded as
// verified: a verification that never ran and one that found nothing wrong look
// the same unless the clean result is recorded too.
func TestVerifyAcceptsWhatTheWriterWritesAndRecordsEveryWindow(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	object(t, s, "security", "acme", start.Add(10*time.Minute),
		copyOf(t, "018f0000-0000-7000-8000-00000000000a", start),
		copyOf(t, "018f0000-0000-7000-8000-00000000000b", start))
	other := copyOf(t, "018f0000-0000-7000-8000-00000000000c", start)
	other.TenantId = "globex"
	object(t, s, "security", "globex", start.Add(70*time.Minute), other)

	run, into := verifier(t, s, start, start.Add(3*time.Hour))
	problems, err := run.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if problems != 0 {
		t.Fatalf("%d problems on a clean archive:\n%s", problems, run.Out)
	}
	verified := events(into, "audit.digest.verified")
	if len(verified) != 2 {
		t.Fatalf("%d verified events for 2 hours with objects", len(verified))
	}
	if want := "records/security/2026/09/17/10"; verified[0].GetTargets()[0].GetId() != want {
		t.Fatalf("target %q, want %q", verified[0].GetTargets()[0].GetId(), want)
	}
	if len(events(into, "audit.digest.failed")) != 0 {
		t.Fatal("a clean archive recorded a failure")
	}
}

// The range is of ingest time and its end is not included.
func TestVerifyChecksOnlyTheRangeOfIngestTime(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	object(t, s, "security", "acme", start.Add(10*time.Minute), copyOf(t, "018f0000-0000-7000-8000-00000000000a", start))
	bad := object(t, s, "security", "acme", start.Add(70*time.Minute), copyOf(t, "018f0000-0000-7000-8000-00000000000b", start))
	s.Replace(bad, []byte("not what was written"))

	run, _ := verifier(t, s, start, start.Add(time.Hour))
	if problems, err := run.Run(context.Background()); err != nil || problems != 0 {
		t.Fatalf("the hour asked for: %d problems, %v", problems, err)
	}
	run, _ = verifier(t, s, start, start.Add(2*time.Hour))
	if problems, err := run.Run(context.Background()); err != nil || problems == 0 {
		t.Fatalf("the corrupt object's hour: %d problems, %v", problems, err)
	}
}

func problemsOf(t *testing.T, s *storetest.Memory, from time.Time) (string, *collector) {
	t.Helper()
	out := &strings.Builder{}
	run, into := verifier(t, s, from, from.Add(time.Hour))
	run.Out = out
	n, err := run.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatalf("a fault was not reported:\n%s", out)
	}
	return out.String(), into
}

// The failure is the event that matters most, and it has to name the hour and
// carry the reason: a reader is asking which hour is in doubt and why.
func TestVerifyReportsAnObjectThatChanged(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	key := object(t, s, "security", "acme", start.Add(10*time.Minute), copyOf(t, "018f0000-0000-7000-8000-00000000000a", start))

	// The same lines, recompressed: the bytes are not the ones the metadata
	// vouches for, though every record in them is intact.
	original, _ := s.Get(context.Background(), key)
	lines, err := recobj.Decode(original)
	if err != nil {
		t.Fatal(err)
	}
	var again [][]byte
	for _, l := range lines {
		again = append(again, recobj.EncodeLine(l.Record))
	}
	again = append(again, recobj.EncodeLine(l0(lines)))
	changed, _ := recobj.Encode(again)
	s.Replace(key, changed)

	report, into := problemsOf(t, s, start)
	if !strings.Contains(report, "sha256 is") || !strings.Contains(report, "count is") {
		t.Fatalf("the report does not say what changed:\n%s", report)
	}
	bad := events(into, "audit.digest.failed")
	if len(bad) != 1 {
		t.Fatalf("%d failure events, want 1 for the one hour in doubt", len(bad))
	}
	if bad[0].GetOutcome().GetResult() != auditv1.Outcome_RESULT_FAILURE || bad[0].GetOutcome().GetReason() == "" {
		t.Fatalf("outcome %v", bad[0].GetOutcome())
	}
	if len(events(into, "audit.digest.verified")) != 0 {
		t.Fatal("the hour in doubt was also recorded as verified")
	}
}

func l0(lines []recobj.Line) []byte { return lines[0].Record }

// A record that is not the one its hash names is caught by the per-record hash
// even when the object's own metadata has been made to agree with the bytes.
func TestVerifyReportsARecordWhoseHashIsNotItsOwn(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	key := object(t, s, "security", "acme", start.Add(10*time.Minute), copyOf(t, "018f0000-0000-7000-8000-00000000000a", start))

	original, _ := s.Get(context.Background(), key)
	lines, _ := recobj.Decode(original)
	forged := strings.Replace(string(lines[0].Record), `"wallet.credential.issued"`, `"wallet.credential.revoked"`, 1)
	if forged == string(lines[0].Record) {
		t.Fatal("the fixture does not carry the action it edits")
	}
	line := []byte(`{"hash":"` + lines[0].Hash + `","record":` + forged + `}`)
	body, meta := recobj.Encode([][]byte{line})
	// Metadata made consistent with the forged bytes: only the record hash can
	// give it away.
	s.Replace(key, body)
	s.SetMetadata(key, meta)

	report, _ := problemsOf(t, s, start)
	if !strings.Contains(report, "line 1") || !strings.Contains(report, "hashes to") {
		t.Fatalf("the report does not name the line:\n%s", report)
	}
}

// A key outside the grammar, or one whose ULID is from another hour, is a
// finding: the key is the only thing a follower orders by.
func TestVerifyReportsAKeyThatBreaksTheGrammar(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	r := copyOf(t, "018f0000-0000-7000-8000-00000000000a", start)
	canonical, _ := record.Canonical(r)
	body, meta := recobj.Encode([][]byte{recobj.EncodeLine(canonical)})
	// The hour says 10 and the ULID was made in hour 11.
	key := store.HourPrefix("security", "acme", start) + ulid.From(start.Add(time.Hour), 1)
	if err := s.Put(context.Background(), store.Object{Key: key, Body: body, Metadata: meta, RetainUntil: start.AddDate(1, 0, 0)}); err != nil {
		t.Fatal(err)
	}
	report, _ := problemsOf(t, s, start)
	if !strings.Contains(report, "ULID") {
		t.Fatalf("the report does not name the ULID:\n%s", report)
	}
}

// With the deployment's lock mode, an object with no lock is INVALID under a
// profile that demands one and merely `unlocked` under one that does not.
func TestVerifyHoldsTheLockToTheProfile(t *testing.T) {
	s := storetest.NewMemory()
	s.Unlocked = true
	start := at(t, "2026-09-17T10:00:00Z")
	object(t, s, "security", "acme", start.Add(10*time.Minute), copyOf(t, "018f0000-0000-7000-8000-00000000000a", start))

	run, _ := verifier(t, s, start, start.Add(time.Hour))
	run.RequiredLock = map[string]string{"security": "compliance"}
	if problems, _ := run.Run(context.Background()); problems != 1 {
		t.Fatalf("%d problems for an unlocked object under a compliance profile", problems)
	}
	run.RequiredLock = map[string]string{"security": "none"}
	out := &strings.Builder{}
	run.Out = out
	if problems, _ := run.Run(context.Background()); problems != 0 || !strings.Contains(out.String(), "unlocked") {
		t.Fatalf("%d problems under a profile that demands none:\n%s", problems, out)
	}
}

// Without a writer the job runs and records nothing, which is what an operator
// at a terminal wants.
func TestVerifyRecordsNothingWithoutAWriter(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	object(t, s, "security", "acme", start.Add(10*time.Minute), copyOf(t, "018f0000-0000-7000-8000-00000000000a", start))
	run, _ := verifier(t, s, start, start.Add(time.Hour))
	run.Sink = nil
	if _, err := run.Run(context.Background()); err != nil {
		t.Fatalf("a verification without a writer must still run: %v", err)
	}
}
