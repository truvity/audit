package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/audit/internal/ulid"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/storetest"
)

var hour = time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

func TestAKeyRoundTripsThroughItsGrammar(t *testing.T) {
	id := ulid.From(hour.Add(13*time.Minute), 1)
	key := store.RecordKey("security", "@platform", hour.Add(13*time.Minute), id)
	if want := "records/security/@platform/2026/09/17/10/" + id; key != want {
		t.Fatalf("key = %s, want %s", key, want)
	}
	got, ok := store.ParseRecordKey(key)
	if !ok || got.Profile != "security" || got.Tenant != "@platform" || !got.Hour.Equal(hour) || got.ULID != id {
		t.Fatalf("parsed %+v, %v", got, ok)
	}
}

func TestWhatIsNotAKeyOfTheGrammarIsRefused(t *testing.T) {
	id := ulid.From(hour, 1)
	elsewhere := ulid.From(hour.Add(time.Hour), 1)
	for name, key := range map[string]string{
		"another prefix":             "catalogue/app/1.0.0",
		"the v0 shape":               "profile=security/tenant=acme/year=2026/month=09/day=17/a.ndjson.zst",
		"a missing component":        "records/security/acme/2026/09/17/" + id,
		"an extra component":         "records/security/acme/2026/09/17/10/x/" + id,
		"an empty tenant":            "records/security//2026/09/17/10/" + id,
		"a non-numeric hour":         "records/security/acme/2026/09/17/1a/" + id,
		"a short year":               "records/security/acme/26/09/17/10/" + id,
		"a date that does not exist": "records/security/acme/2026/02/30/10/" + id,
		"an hour past 23":            "records/security/acme/2026/09/17/24/" + id,
		"a ULID that is not one":     "records/security/acme/2026/09/17/10/not-a-ulid",
		"a ULID from another hour":   "records/security/acme/2026/09/17/10/" + elsewhere,
	} {
		if _, ok := store.ParseRecordKey(key); ok {
			t.Errorf("%s: %s was accepted", name, key)
		}
	}
}

func TestKeyComponent(t *testing.T) {
	for _, ok := range []string{"security", "@platform", "acme-eu", "a.b"} {
		if why := store.KeyComponent(ok); why != "" {
			t.Errorf("%q refused: %s", ok, why)
		}
	}
	for _, bad := range []string{"", "a/b", "/", ".", ".."} {
		if store.KeyComponent(bad) == "" {
			t.Errorf("%q accepted", bad)
		}
	}
}

func put(t *testing.T, s *storetest.Memory, profile, tenant string, at time.Time, serial uint64) string {
	t.Helper()
	key := store.RecordKey(profile, tenant, at, ulid.From(at, serial))
	if err := s.Put(context.Background(), store.Object{Key: key, Body: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	return key
}

// The walk is one listing over a tenant, started after the hour before the
// range and stopped at the first key past it.
func TestWalkHoursVisitsTheRangeInKeyOrder(t *testing.T) {
	s := storetest.NewMemory()
	var inside []string
	put(t, s, "security", "acme", hour.Add(-time.Hour), 1)
	inside = append(inside, put(t, s, "security", "acme", hour, 2), put(t, s, "security", "acme", hour, 3))
	inside = append(inside, put(t, s, "security", "acme", hour.Add(time.Hour), 1))
	put(t, s, "security", "acme", hour.Add(2*time.Hour), 1)
	put(t, s, "security", "globex", hour, 1)
	put(t, s, "history", "acme", hour, 1)

	var got []string
	err := store.WalkHours(context.Background(), s, "security", "acme", hour.Add(10*time.Minute), hour.Add(time.Hour+30*time.Minute), "",
		func(e store.Entry) error { got = append(got, e.Key); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(inside) {
		t.Fatalf("walked %v, want %v", got, inside)
	}
	for i := range got {
		if got[i] != inside[i] {
			t.Fatalf("walked %v, want %v", got, inside)
		}
	}

	// Resumed after the first key, it gives the rest.
	got = nil
	err = store.WalkHours(context.Background(), s, "security", "acme", hour, hour.Add(time.Hour), inside[0],
		func(e store.Entry) error { got = append(got, e.Key); return nil })
	if err != nil || len(got) != 2 || got[0] != inside[1] {
		t.Fatalf("resumed: %v, %v", got, err)
	}

	// An empty range visits nothing.
	got = nil
	_ = store.WalkHours(context.Background(), s, "security", "acme", hour.Add(time.Hour), hour, "",
		func(e store.Entry) error { got = append(got, e.Key); return nil })
	if len(got) != 0 {
		t.Fatalf("an empty range walked %v", got)
	}
}

func TestWalkProfileVisitsEveryTenantOfOneProfile(t *testing.T) {
	s := storetest.NewMemory()
	put(t, s, "security", "acme", hour, 1)
	put(t, s, "security", "globex", hour, 1)
	put(t, s, "security", "globex", hour, 2)
	put(t, s, "history", "acme", hour, 1)

	seen := map[string]int{}
	err := store.WalkProfile(context.Background(), s, "security", hour, hour, func(tenant string, _ store.Entry) error {
		seen[tenant]++
		return nil
	})
	if err != nil || seen["acme"] != 1 || seen["globex"] != 2 || len(seen) != 2 {
		t.Fatalf("saw %v, %v", seen, err)
	}
	profiles, err := store.Profiles(context.Background(), s)
	if err != nil || len(profiles) != 2 || profiles[0] != "history" || profiles[1] != "security" {
		t.Fatalf("profiles %v, %v", profiles, err)
	}
}
