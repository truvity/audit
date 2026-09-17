package writer_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store"
)

func day(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return at.UTC()
}

func copyFor(t *testing.T, profile, tenant string, at time.Time) *record.Record {
	t.Helper()
	r := issued(t)
	r.Profile = profile
	r.TenantId = tenant
	r.OccurredAt = timestamppb.New(at)
	return r
}

func roller(t *testing.T, s store.Store, now func() time.Time) *writer.Roller {
	t.Helper()
	r := &writer.Roller{Store: s, Instance: "writer-1", Now: now}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// A lifecycle rule matches a literal prefix, so the profile has to come first
// for a per-profile rule to be expressible at all.
func TestObjectKeysArePartitionedProfileFirst(t *testing.T) {
	s := store.NewMemory()
	at := day(t, "2026-09-17T10:30:00Z")
	r := roller(t, s, func() time.Time { return at })
	p := profiles(t)["security"]

	if err := r.Add(context.Background(), p, copyFor(t, "security", "acme", at)); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	keys := s.Keys()
	if len(keys) != 1 {
		t.Fatalf("keys = %v", keys)
	}
	key := keys[0]
	for _, want := range []string{
		"profile=security/", "tenant=acme/", "year=2026/", "month=09/", "day=17/",
		"-writer-1-", ".ndjson.zst",
	} {
		if !strings.Contains(key, want) {
			t.Errorf("key %q lacks %q", key, want)
		}
	}
	if !strings.HasPrefix(key, "profile=security/") {
		t.Fatalf("key %q must begin with the profile, or no lifecycle rule can target it", key)
	}
}

// One object holds one profile, for one tenant, for one day.
func TestRollerSeparatesProfilesTenantsAndDays(t *testing.T) {
	s := store.NewMemory()
	at := day(t, "2026-09-17T10:30:00Z")
	r := roller(t, s, func() time.Time { return at })
	all := profiles(t)

	ctx := context.Background()
	for _, c := range []struct {
		profile string
		tenant  string
		at      time.Time
	}{
		{"security", "acme", at},
		{"security", "acme", at.Add(time.Minute)},
		{"security", "other", at},
		{"billing", "acme", at},
		{"security", "acme", at.AddDate(0, 0, -1)},
	} {
		if err := r.Add(ctx, all[c.profile], copyFor(t, c.profile, c.tenant, c.at)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if n := s.Len(); n != 4 {
		t.Fatalf("wrote %d objects, want one per profile, tenant and day: %v", n, s.Keys())
	}
}

// Every copy in an object is kept at least as long as the profile asks of the
// oldest, so the retention is fixed when the object is opened.
func TestObjectsCarryTheProfilesRetention(t *testing.T) {
	s := store.NewMemory()
	at := day(t, "2026-09-17T10:30:00Z")
	r := roller(t, s, func() time.Time { return at })
	all := profiles(t)
	ctx := context.Background()

	for name := range all {
		if err := r.Add(ctx, all[name], copyFor(t, name, "acme", at)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for _, key := range s.Keys() {
		o, _ := s.Object(key)
		profile := o.Metadata["audit-profile"]
		want := all[profile].RetainUntil(at, nil)
		if !o.RetainUntil.Equal(want) {
			t.Errorf("%s retains until %s, want %s", profile, o.RetainUntil, want)
		}
	}
	// The billing copy is the seven-year tax record; the security copy is not.
	var billing, security time.Time
	for _, key := range s.Keys() {
		o, _ := s.Object(key)
		switch o.Metadata["audit-profile"] {
		case "billing":
			billing = o.RetainUntil
		case "security":
			security = o.RetainUntil
		}
	}
	if !billing.After(security.AddDate(5, 0, 0)) {
		t.Fatalf("billing retains until %s, security until %s: the tax record is not longer", billing, security)
	}
}

// An object is written once. Under Object Lock a second put creates a version
// rather than replacing anything, so a writer that reuses a key writes objects
// a digest cannot account for.
func TestRollerNeverReusesAKey(t *testing.T) {
	s := store.NewMemory()
	at := day(t, "2026-09-17T10:30:00Z")
	r := roller(t, s, func() time.Time { return at })
	p := profiles(t)["security"]
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		if err := r.Add(ctx, p, copyFor(t, "security", "acme", at)); err != nil {
			t.Fatal(err)
		}
		if err := r.Flush(ctx); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	if s.Len() != 20 {
		t.Fatalf("wrote %d objects for 20 flushes: a key was reused", s.Len())
	}
}

func TestRollerRollsOnSize(t *testing.T) {
	s := store.NewMemory()
	at := day(t, "2026-09-17T10:30:00Z")
	r := roller(t, s, func() time.Time { return at })
	r.MaxBytes = 2000
	p := profiles(t)["security"]
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		if err := r.Add(ctx, p, copyFor(t, "security", "acme", at)); err != nil {
			t.Fatal(err)
		}
	}
	if s.Len() == 0 {
		t.Fatal("nothing rolled: a full object must be written without waiting for the timer")
	}
	if !strings.HasSuffix(s.Keys()[0], ".ndjson.zst") {
		t.Fatalf("key = %q", s.Keys()[0])
	}
}

func TestRollerRollsOnTime(t *testing.T) {
	s := store.NewMemory()
	at := day(t, "2026-09-17T10:30:00Z")
	now := at
	r := roller(t, s, func() time.Time { return now })
	r.Interval = time.Minute
	p := profiles(t)["security"]
	ctx := context.Background()

	if err := r.Add(ctx, p, copyFor(t, "security", "acme", at)); err != nil {
		t.Fatal(err)
	}
	if r.Due() {
		t.Fatal("a fresh object is not due")
	}
	now = at.Add(2 * time.Minute)
	if !r.Due() {
		t.Fatal("an object open past the interval is due")
	}
	if err := r.Add(ctx, p, copyFor(t, "security", "acme", now)); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 1 {
		t.Fatalf("wrote %d objects; the old one should have rolled on the next add", s.Len())
	}
}

// The object holds exactly the copies that went into it, readable by anything
// that can decompress.
func TestObjectHoldsTheCopies(t *testing.T) {
	s := store.NewMemory()
	at := day(t, "2026-09-17T10:30:00Z")
	r := roller(t, s, func() time.Time { return at })
	p := profiles(t)["security"]
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := r.Add(ctx, p, copyFor(t, "security", "acme", at)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	body, err := s.Get(ctx, s.Keys()[0])
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	plain, err := decoder.DecodeAll(body, nil)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(plain), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("the object holds %d lines, want 3", len(lines))
	}
	for i, line := range lines {
		var got record.Record
		if err := record.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d does not parse: %v", i, err)
		}
		if got.GetProfile() != "security" {
			t.Fatalf("line %d is from profile %q", i, got.GetProfile())
		}
	}
	o, _ := s.Object(s.Keys()[0])
	if o.Metadata["audit-records"] != "3" {
		t.Fatalf("the object does not say what it holds: %v", o.Metadata)
	}
}

// A store that will not take an object must not lose the copies the writer has
// already taken responsibility for.
func TestAFailedPutKeepsTheCopies(t *testing.T) {
	s := store.NewMemory()
	s.FailPut = errors.New("the bucket is unreachable")
	at := day(t, "2026-09-17T10:30:00Z")
	r := roller(t, s, func() time.Time { return at })
	p := profiles(t)["security"]
	ctx := context.Background()

	if err := r.Add(ctx, p, copyFor(t, "security", "acme", at)); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(ctx); err == nil {
		t.Fatal("want the store's error")
	}
	if r.Pending() != 1 {
		t.Fatalf("pending = %d; a failed put must keep the copies", r.Pending())
	}

	s.FailPut = nil
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 1 || r.Pending() != 0 {
		t.Fatalf("after recovery: %d objects, %d pending", s.Len(), r.Pending())
	}
}
