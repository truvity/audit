package cli_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/catalogue"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/digest"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/record"
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

// A chain that is never sealed and a chain sealed over an empty hour look
// identical in the archive. The event is the difference, and a quiet hour is
// exactly the one worth being able to prove.
func TestDigestRecordsEveryWindowItSealed(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	archived(t, s, "acme", start.Add(30*time.Minute), "a.ndjson.zst")

	into := &collector{}
	run := sealer(t, s, start.Add(3*time.Hour))
	run.From = start
	run.Sink, run.Catalogue, run.Instance = into, common(t), "digest-1"
	if _, err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	written := events(into, "audit.digest.written")
	if len(written) != 3 {
		t.Fatalf("%d events for 3 sealed windows", len(written))
	}
	first := written[0]
	if first.GetOutcome().GetResult() != auditv1.Outcome_RESULT_SUCCESS {
		t.Fatalf("outcome %v", first.GetOutcome())
	}
	if len(first.GetTargets()) != 1 || first.GetTargets()[0].GetType() != "digest" {
		t.Fatalf("targets %v", first.GetTargets())
	}
	if want := digest.Key("security", start); first.GetTargets()[0].GetId() != want {
		t.Fatalf("target %q, want the digest %q", first.GetTargets()[0].GetId(), want)
	}
	data := first.GetData().AsMap()
	if got := data["objects"]; got != float64(1) {
		t.Fatalf("objects %v, want 1", got)
	}
	if got := data["window_start"]; got != start.Format(time.RFC3339) {
		t.Fatalf("window_start %v, want %s", got, start.Format(time.RFC3339))
	}

	// The quiet hours are recorded with zero objects, not skipped.
	if got := written[1].GetData().AsMap()["objects"]; got != float64(0) {
		t.Fatalf("a quiet window recorded %v objects", got)
	}
}

// A window already sealed produces no second event: the event says a digest was
// written, and none was.
func TestDigestRecordsNothingForAWindowAlreadySealed(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	archived(t, s, "acme", start.Add(30*time.Minute), "a.ndjson.zst")

	into := &collector{}
	run := sealer(t, s, start.Add(time.Hour))
	run.Sink, run.Catalogue, run.Instance = into, common(t), "digest-1"
	if _, err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	run.From = start
	if _, err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(events(into, "audit.digest.written")); got != 1 {
		t.Fatalf("%d events for one window sealed once", got)
	}
}

// verified returns a verifier over a sealed chain, and what it records into.
func verified(t *testing.T, s *storetest.Memory, signer *keys.LocalSigner, from, to time.Time) (cli.Verify, *collector) {
	t.Helper()
	pub, err := signer.PublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	into := &collector{}
	return cli.Verify{
		Store: s, PublicKeyPEM: pub, Profile: "security", From: from, To: to,
		Sink: into, Catalogue: common(t), Instance: "verify-1",
		Out: &strings.Builder{},
	}, into
}

// A verification that never ran and one that found nothing wrong look the same
// in the archive unless the clean result is recorded too.
func TestVerifyRecordsEveryWindowItChecked(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	archived(t, s, "acme", start.Add(30*time.Minute), "a.ndjson.zst")

	run := sealer(t, s, start.Add(2*time.Hour))
	run.From = start
	if _, err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	signer, ok := run.Signer.(*keys.LocalSigner)
	if !ok {
		t.Fatalf("%T", run.Signer)
	}

	check, into := verified(t, s, signer, start, start.Add(2*time.Hour))
	problems, err := check.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if problems != 0 {
		t.Fatalf("%d problems on a clean chain", problems)
	}
	ok2 := events(into, "audit.digest.verified")
	if len(ok2) != 2 {
		t.Fatalf("%d verified events for 2 windows checked", len(ok2))
	}
	if len(events(into, "audit.digest.failed")) != 0 {
		t.Fatal("a clean chain recorded a failure")
	}
	if ok2[0].GetTargets()[0].GetType() != "digest" {
		t.Fatalf("target type %q", ok2[0].GetTargets()[0].GetType())
	}
}

// The failure is the event that matters most, and it has to name the window and
// carry the reason: a reader is asking which hour is in doubt and why.
func TestVerifyRecordsTheWindowThatFailedAndWhy(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	key := archived(t, s, "acme", start.Add(30*time.Minute), "a.ndjson.zst")

	run := sealer(t, s, start.Add(time.Hour))
	if _, err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	signer, ok := run.Signer.(*keys.LocalSigner)
	if !ok {
		t.Fatalf("%T", run.Signer)
	}
	// The object changes after it was sealed.
	s.Replace(key, []byte("not what was signed"))

	check, into := verified(t, s, signer, start, start.Add(time.Hour))
	problems, err := check.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if problems == 0 {
		t.Fatal("a changed object was not reported")
	}
	bad := events(into, "audit.digest.failed")
	if len(bad) != 1 {
		t.Fatalf("%d failure events, want 1 for the one window in doubt", len(bad))
	}
	if bad[0].GetOutcome().GetResult() != auditv1.Outcome_RESULT_FAILURE {
		t.Fatalf("outcome %v", bad[0].GetOutcome())
	}
	if bad[0].GetOutcome().GetReason() == "" {
		t.Fatal("the failure carries no reason, which is what a reader needs")
	}
	if want := digest.Key("security", start); bad[0].GetTargets()[0].GetId() != want {
		t.Fatalf("target %q, want %q", bad[0].GetTargets()[0].GetId(), want)
	}
	if len(events(into, "audit.digest.verified")) != 0 {
		t.Fatal("the window in doubt was also recorded as verified")
	}
}

// An object nothing accounts for is attributed to the window that should have
// covered it, so the failure names an hour rather than the whole profile.
func TestVerifyBlamesTheWindowThatShouldHaveCoveredAnObject(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	archived(t, s, "acme", start.Add(10*time.Minute), "a.ndjson.zst")

	run := sealer(t, s, start.Add(time.Hour))
	if _, err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	signer, ok := run.Signer.(*keys.LocalSigner)
	if !ok {
		t.Fatalf("%T", run.Signer)
	}
	// Written into the sealed window after it was sealed.
	stray := archived(t, s, "globex", start.Add(20*time.Minute), "b.ndjson.zst")

	check, into := verified(t, s, signer, start, start.Add(time.Hour))
	if _, err := check.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad := events(into, "audit.digest.failed")
	if len(bad) != 1 {
		t.Fatalf("%d failure events, want 1", len(bad))
	}
	if want := digest.Key("security", start); bad[0].GetTargets()[0].GetId() != want {
		t.Fatalf("blamed %q, want the window that should have covered it, %q",
			bad[0].GetTargets()[0].GetId(), want)
	}
	if !strings.Contains(bad[0].GetOutcome().GetReason(), stray) {
		t.Fatalf("the reason does not name the object: %q", bad[0].GetOutcome().GetReason())
	}
}

// Without a writer the jobs run and record nothing, which is what an operator
// at a terminal wants.
func TestTheJobsRecordNothingWithoutAWriter(t *testing.T) {
	s := storetest.NewMemory()
	start := at(t, "2026-09-17T10:00:00Z")
	archived(t, s, "acme", start.Add(30*time.Minute), "a.ndjson.zst")

	run := sealer(t, s, start.Add(time.Hour))
	if _, err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	signer, ok := run.Signer.(*keys.LocalSigner)
	if !ok {
		t.Fatalf("%T", run.Signer)
	}
	check, _ := verified(t, s, signer, start, start.Add(time.Hour))
	check.Sink, check.Catalogue = nil, nil
	if _, err := check.Run(context.Background()); err != nil {
		t.Fatalf("a verification without a writer must still run: %v", err)
	}
}
