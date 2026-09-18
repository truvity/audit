package cli_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/preset"
)

// marker stands in for the deduplication table, which a purge only tells a
// moment to forget before.
type marker struct{ before time.Time }

func (m *marker) Purge(_ context.Context, before time.Time) error {
	m.before = before
	return nil
}

func purger(t *testing.T, target index.Indexer, dedupe cli.Marker, now time.Time) cli.Purge {
	t.Helper()
	presets, err := preset.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := (&preset.Deployment{
		Profiles: map[string]preset.ProfileConfig{"security": {Presets: []string{"security"}}},
	}).Compose(presets)
	if err != nil {
		t.Fatal(err)
	}
	return cli.Purge{
		Index: target, Dedupe: dedupe, Profiles: profiles,
		Now: func() time.Time { return now }, Out: &strings.Builder{},
	}
}

func indexRow(t *testing.T, m *index.Memory, id string, recorded time.Time) {
	t.Helper()
	err := m.Index(context.Background(), "security", []index.Row{{
		ID: id, TenantID: "acme", RecordedAt: recorded, OccurredAt: recorded,
		Action: "wallet.credential.issued", Operation: "create", Outcome: "success",
		ActorKind: "operator", ActorID: "ps_abc", ClientAddress: "203.0.113.9",
		ObjectKey: "k", Line: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
}

// The schedule is the profile's own retention, not a number this command
// chose. A row is past it when it was recorded that long ago.
func TestPurgeDerivesTheScheduleFromTheProfile(t *testing.T) {
	now := at(t, "2026-09-17T10:00:00Z")
	target := index.NewMemory()
	run := purger(t, target, &marker{}, now)
	run.DryRun = true

	report, err := run.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Profiles) != 1 {
		t.Fatalf("%d profiles", len(report.Profiles))
	}
	retention := run.Profiles["security"].RetainUntil(now, nil).Sub(now)
	if want := now.Add(-retention); !report.Profiles[0].Everything.Equal(want) {
		t.Fatalf("purging before %s, want %s", report.Profiles[0].Everything, want)
	}
	if retention <= 0 {
		t.Fatal("the security profile has no retention, so this test proves nothing")
	}
}

// A row past its profile's retention goes; one inside it stays.
func TestPurgeRemovesOnlyWhatIsPastRetention(t *testing.T) {
	now := at(t, "2026-09-17T10:00:00Z")
	target := index.NewMemory()
	run := purger(t, target, &marker{}, now)
	retention := run.Profiles["security"].RetainUntil(now, nil).Sub(now)

	indexRow(t, target, "old", now.Add(-retention).Add(-time.Hour))
	indexRow(t, target, "recent", now.Add(-time.Hour))
	if _, err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, found := target.Row("security", "old"); found {
		t.Fatal("a row past the profile's retention was kept")
	}
	if _, found := target.Row("security", "recent"); !found {
		t.Fatal("a row inside the profile's retention was removed")
	}
}

// No shipped preset states a separate, shorter life for who an event happened
// to. Rather than invent one, the command forgets nothing early unless a
// deployment says how long — and then it keeps the event.
func TestPurgeForgetsIdentitiesOnlyWhenToldHowLong(t *testing.T) {
	now := at(t, "2026-09-17T10:00:00Z")
	target := index.NewMemory()
	run := purger(t, target, &marker{}, now)
	indexRow(t, target, "a", now.Add(-48*time.Hour))

	if _, err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	row, found := target.Row("security", "a")
	if !found {
		t.Fatal("the row is well inside the retention and must stay")
	}
	if row.ActorID == "" {
		t.Fatal("identities were forgotten on a schedule nobody asked for")
	}

	run.IdentifyingAfter = 24 * time.Hour
	report, err := run.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Profiles[0].Identifying == nil {
		t.Fatal("the report does not say when identities were forgotten")
	}
	row, found = target.Row("security", "a")
	if !found {
		t.Fatal("forgetting who it happened to removed what happened")
	}
	if row.ActorID != "" || row.ClientAddress != "" {
		t.Fatalf("identifying columns survived: %+v", row)
	}
	if row.Action == "" || row.ActorKind == "" {
		t.Fatal("what happened, and what kind of actor did it, must survive")
	}
}

// A dry run is for an operator about to forget something irreversibly.
func TestPurgeDryRunForgetsNothing(t *testing.T) {
	now := at(t, "2026-09-17T10:00:00Z")
	target := index.NewMemory()
	dedupe := &marker{}
	run := purger(t, target, dedupe, now)
	retention := run.Profiles["security"].RetainUntil(now, nil).Sub(now)
	indexRow(t, target, "old", now.Add(-retention).Add(-time.Hour))

	run.DryRun = true
	run.DedupeWindow = 14 * 24 * time.Hour
	report, err := run.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, found := target.Row("security", "old"); !found {
		t.Fatal("a dry run purged a row")
	}
	if !dedupe.before.IsZero() {
		t.Fatal("a dry run purged the deduplication table")
	}
	if report.Dedupe == nil || !report.Dedupe.Equal(now.Add(-14*24*time.Hour)) {
		t.Fatalf("the report does not say what would have been forgotten: %+v", report.Dedupe)
	}
}

// The deduplication table is otherwise unbounded, and the window is what bounds
// it.
func TestPurgeForgetsIdentifiersPastTheWindow(t *testing.T) {
	now := at(t, "2026-09-17T10:00:00Z")
	dedupe := &marker{}
	run := purger(t, index.NewMemory(), dedupe, now)
	run.DedupeWindow = 14 * 24 * time.Hour

	if _, err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-14 * 24 * time.Hour); !dedupe.before.Equal(want) {
		t.Fatalf("forgot identifiers before %s, want %s", dedupe.before, want)
	}
}
