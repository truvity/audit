package cli_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/hold"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/storetest"
)

// holder is a hold command over an archive with one object in it.
func holder(t *testing.T, into sink.Sink) (cli.Hold, *storetest.Memory) {
	t.Helper()
	s := storetest.NewMemory()
	if err := s.Put(context.Background(), store.Object{
		Key:  "profile=security/tenant=acme/year=2026/month=09/day=17/a.ndjson.zst",
		Body: []byte("{}"), RetainUntil: at(t, "2027-09-17T00:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	return cli.Hold{
		Store: s, Sink: into, Catalogue: common(t),
		Profile: "security", Tenant: "acme", Reason: "matter 2026-11", ID: "h-1", By: "olga",
		Out: &strings.Builder{},
	}, s
}

// Placing and releasing are recorded, confirmed, and name the operator.
func TestAHoldAndItsReleaseAreRecorded(t *testing.T) {
	into := &collector{}
	run, s := holder(t, into)
	ctx := context.Background()

	if err := run.Run(ctx, "place"); err != nil {
		t.Fatal(err)
	}
	if held := s.HeldKeys(); len(held) != 1 {
		t.Fatalf("held %v", held)
	}
	if err := run.Run(ctx, "release"); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"audit.hold.placed", "audit.hold.released"} {
		got := events(into, action)
		if len(got) != 1 {
			t.Fatalf("%s: %d events", action, len(got))
		}
		e := got[0]
		if e.GetOutcome().GetResult() != auditv1.Outcome_RESULT_SUCCESS ||
			e.GetActor().GetId() != "olga" || e.GetActor().GetKind() != "operator" ||
			e.GetTargets()[0].GetId() != "h-1" {
			t.Fatalf("%s: %+v", action, e)
		}
	}
}

// The catalogue declares both block. A hold placed or released off the record
// is one nobody can account for, so without a writer neither happens at all.
func TestAHoldWithoutAWriterIsRefusedBeforeAnythingChanges(t *testing.T) {
	run, s := holder(t, nil)
	run.Catalogue = nil
	for _, action := range []string{"place", "release"} {
		err := run.Run(context.Background(), action)
		if err == nil || !strings.Contains(err.Error(), "--sink") {
			t.Fatalf("%s without a writer: %v", action, err)
		}
	}
	if held := s.HeldKeys(); len(held) != 0 {
		t.Fatalf("a refused place held %v", held)
	}
	// Listing reads only, and needs no writer.
	if err := run.Run(context.Background(), "list"); err != nil {
		t.Fatalf("list without a writer: %v", err)
	}
}

// A writer that will not take the record makes the command fail, and the error
// says the hold changed anyway — the operator must not walk away believing
// nothing happened.
func TestAHoldTheTrailCannotTakeSaysItIsPlaced(t *testing.T) {
	refusing := sink.Func(func(context.Context, *sink.Request) (*sink.Result, error) {
		return nil, errors.New("the writer is unreachable")
	})
	run, s := holder(t, refusing)
	err := run.Run(context.Background(), "place")
	if err == nil || !strings.Contains(err.Error(), "IS PLACED") {
		t.Fatalf("want an error saying the hold is placed anyway, got %v", err)
	}
	if held := s.HeldKeys(); len(held) != 1 {
		t.Fatalf("the error says placed; the archive says %v", held)
	}
}

// A release the archive refuses — the designed outcome for anyone without the
// break-glass role — is still in the trail, as a failed attempt.
func TestARefusedReleaseIsRecordedAsAFailure(t *testing.T) {
	into := &collector{}
	run, s := holder(t, into)
	ctx := context.Background()
	if _, err := (hold.Store{Store: s}).Place(ctx, hold.Record{
		ID: "h-1", Profile: "security", Tenant: "acme", Reason: "matter 2026-11", PlacedBy: "olga",
	}); err != nil {
		t.Fatal(err)
	}
	s.FailPut = errors.New("AccessDenied: only the break-glass role may release a hold")

	if err := run.Run(ctx, "release"); err == nil {
		t.Fatal("a refused release reported success")
	}
	got := events(into, "audit.hold.released")
	if len(got) != 1 {
		t.Fatalf("%d records of the refused release", len(got))
	}
	if o := got[0].GetOutcome(); o.GetResult() != auditv1.Outcome_RESULT_FAILURE ||
		!strings.Contains(o.GetReason(), "break-glass") {
		t.Fatalf("outcome %+v", o)
	}
	if held := s.HeldKeys(); len(held) != 1 {
		t.Fatalf("the refused release cleared %v", held)
	}
}
