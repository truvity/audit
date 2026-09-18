package hold_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/internal/hold"
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

func archive(t *testing.T) (*storetest.Memory, hold.Store) {
	t.Helper()
	s := storetest.NewMemory()
	return s, hold.Store{
		Store: s,
		Now:   func() time.Time { return at(t, "2026-09-17T10:00:00Z") },
	}
}

// object writes an archive object as the writer would.
func object(t *testing.T, s *storetest.Memory, profile, tenant, name string) string {
	t.Helper()
	key := "profile=" + profile + "/tenant=" + tenant + "/year=2026/month=09/day=17/" + name
	if err := s.Put(context.Background(), store.Object{
		Key: key, Body: []byte("{}"), RetainUntil: at(t, "2027-09-17T00:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	return key
}

// A hold covers every tenant of the profile it names, because that is what a
// hold on a profile means and the archive puts the tenant below it.
func TestPlaceHoldsEveryTenantOfAProfile(t *testing.T) {
	s, holds := archive(t)
	acme := object(t, s, "security", "acme", "a.ndjson.zst")
	globex := object(t, s, "security", "globex", "b.ndjson.zst")
	other := object(t, s, "history", "acme", "c.ndjson.zst")

	placed, err := holds.Place(context.Background(), hold.Record{
		ID: "h-1", Profile: "security", Reason: "matter 2026-11", PlacedBy: "olga",
	})
	if err != nil {
		t.Fatal(err)
	}
	if placed.Objects != 2 {
		t.Fatalf("swept %d objects, want the 2 under security", placed.Objects)
	}
	held := map[string]bool{}
	for _, k := range s.HeldKeys() {
		held[k] = true
	}
	if !held[acme] || !held[globex] {
		t.Fatalf("a tenant of the held profile was missed: %v", s.HeldKeys())
	}
	if held[other] {
		t.Fatal("a profile nobody held was held")
	}
}

// Narrowed to a tenant, it holds that tenant and no other.
func TestPlaceCanHoldOneTenant(t *testing.T) {
	s, holds := archive(t)
	acme := object(t, s, "security", "acme", "a.ndjson.zst")
	globex := object(t, s, "security", "globex", "b.ndjson.zst")

	if _, err := holds.Place(context.Background(), hold.Record{
		ID: "h-1", Profile: "security", Tenant: "acme", Reason: "matter", PlacedBy: "olga",
	}); err != nil {
		t.Fatal(err)
	}
	held := s.HeldKeys()
	if len(held) != 1 || held[0] != acme {
		t.Fatalf("held %v, want only %s", held, acme)
	}
	_ = globex
}

// A hold nobody can account for cannot be safely released: whoever finds it
// later has no way to know whether the matter is over.
func TestPlaceRefusesWithoutAReason(t *testing.T) {
	_, holds := archive(t)
	_, err := holds.Place(context.Background(), hold.Record{
		ID: "h-1", Profile: "security", PlacedBy: "olga",
	})
	if err == nil {
		t.Fatal("a hold without a reason must be refused")
	}
	if !strings.Contains(err.Error(), "reason") {
		t.Errorf("the refusal should say what is missing: %v", err)
	}
}

// The record of a hold is in the archive under the same lock as everything
// else, and it is append-only like everything else: placing writes one object,
// releasing writes another, and neither is overwritten.
func TestAHoldIsRecordedTwiceAndOverwrittenNever(t *testing.T) {
	s, holds := archive(t)
	object(t, s, "security", "acme", "a.ndjson.zst")
	ctx := context.Background()

	if _, err := holds.Place(ctx, hold.Record{
		ID: "h-1", Profile: "security", Reason: "matter 2026-11", PlacedBy: "olga",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Head(ctx, hold.PlacedKey("h-1")); err != nil {
		t.Fatalf("the placement was not recorded: %v", err)
	}
	if _, err := s.Head(ctx, hold.ReleasedKey("h-1")); err == nil {
		t.Fatal("a hold still on has a release record")
	}

	released, err := holds.Release(ctx, "h-1", "break-glass")
	if err != nil {
		t.Fatal(err)
	}
	if released.Active() {
		t.Fatal("the hold is still reported active after release")
	}
	if _, err := s.Head(ctx, hold.ReleasedKey("h-1")); err != nil {
		t.Fatalf("the release was not recorded: %v", err)
	}
	if len(s.HeldKeys()) != 0 {
		t.Fatalf("objects are still held after release: %v", s.HeldKeys())
	}

	// Read back: the placement's reason survives, and the release is on it.
	got, err := holds.Get(ctx, "h-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason != "matter 2026-11" || got.PlacedBy != "olga" {
		t.Fatalf("the placement was lost: %+v", got)
	}
	if got.ReleasedBy != "break-glass" || got.Active() {
		t.Fatalf("the release was lost: %+v", got)
	}
}

// Listing folds each hold's two records into one.
func TestListFoldsTheTwoRecords(t *testing.T) {
	s, holds := archive(t)
	object(t, s, "security", "acme", "a.ndjson.zst")
	object(t, s, "history", "acme", "b.ndjson.zst")
	ctx := context.Background()

	for _, c := range []struct{ id, profile string }{{"h-1", "security"}, {"h-2", "history"}} {
		if _, err := holds.Place(ctx, hold.Record{
			ID: c.id, Profile: c.profile, Reason: "matter", PlacedBy: "olga",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := holds.Release(ctx, "h-2", "break-glass"); err != nil {
		t.Fatal(err)
	}

	all, err := holds.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("%d holds, want 2 folded from 3 records", len(all))
	}
	active, err := holds.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != "h-1" {
		t.Fatalf("active: %+v, want only h-1", active)
	}
}

func TestReleaseRefusesTwice(t *testing.T) {
	s, holds := archive(t)
	object(t, s, "security", "acme", "a.ndjson.zst")
	ctx := context.Background()
	if _, err := holds.Place(ctx, hold.Record{
		ID: "h-1", Profile: "security", Reason: "matter", PlacedBy: "olga",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := holds.Release(ctx, "h-1", "break-glass"); err != nil {
		t.Fatal(err)
	}
	if _, err := holds.Release(ctx, "h-1", "break-glass"); err == nil {
		t.Fatal("releasing a released hold must be refused")
	}
}

// The watcher is what tells the writer a prefix is held. Until it has read the
// holds at least once it says so, rather than answering false as though it
// knew there were none.
func TestWatcherSaysWhenItHasNotRead(t *testing.T) {
	s, holds := archive(t)
	object(t, s, "security", "acme", "a.ndjson.zst")
	ctx := context.Background()
	if _, err := holds.Place(ctx, hold.Record{
		ID: "h-1", Profile: "security", Reason: "matter", PlacedBy: "olga",
	}); err != nil {
		t.Fatal(err)
	}

	w := &hold.Watcher{Holds: holds}
	if w.Ready() {
		t.Fatal("a watcher that has read nothing reports itself ready")
	}
	if err := w.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !w.Ready() {
		t.Fatal("a watcher that has read the holds is not ready")
	}
	if !w.Held("security", "acme") || !w.Held("security", "globex") {
		t.Fatal("a profile-wide hold does not cover a tenant")
	}
	if w.Held("history", "acme") {
		t.Fatal("a profile nobody held is reported held")
	}

	if _, err := holds.Release(ctx, "h-1", "break-glass"); err != nil {
		t.Fatal(err)
	}
	if err := w.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if w.Held("security", "acme") {
		t.Fatal("a released hold still covers its profile")
	}
}
