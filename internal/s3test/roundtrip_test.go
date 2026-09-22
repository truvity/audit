package s3test_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/truvity/audit/internal/s3test"
	"github.com/truvity/audit/store"
)

// The archive's round trip -- put, get, head, list, the tenants under a
// profile -- against a real bucket in both shapes the store is deployed in:
// with Object Lock in compliance mode, and with no lock at all. The second is
// what a store without the Object Lock API looks like, and the writer, the
// digest job and the query service all take it; a harness that only ran the
// locked shape would leave that path to the memory store.
func TestRoundTripInBothLockModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		lock bool
	}{{"locked", true}, {"unlocked", false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := s3test.Open(t, tc.lock)
			ctx := context.Background()
			keys := s3test.Fill(t, s, "security", "acme", day, 3)
			s3test.Fill(t, s, "security", "globex", day, 1)

			body, err := s.Get(ctx, keys[0])
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != "{}" {
				t.Fatalf("body = %q", body)
			}
			entry, err := s.Head(ctx, keys[0])
			if err != nil {
				t.Fatal(err)
			}
			// A locked bucket reports the retention the put set; an unlocked
			// one was sent none and reports none, which is what verify reads
			// as `unlocked`.
			if tc.lock && entry.RetainUntil.IsZero() {
				t.Fatal("the locked object carries no retention")
			}
			if !tc.lock && !entry.RetainUntil.IsZero() {
				t.Fatalf("the unlocked object carries a retention %s: a lock header was sent", entry.RetainUntil)
			}
			entries, err := s.List(ctx, "profile=security/tenant=acme/", "", 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 3 {
				t.Fatalf("listed %d of 3", len(entries))
			}
			tenants, err := s.Prefixes(ctx, "profile=security/", "/")
			if err != nil {
				t.Fatal(err)
			}
			if len(tenants) != 2 {
				t.Fatalf("tenants = %v", tenants)
			}
			// A key is written once in either shape.
			err = s.Put(ctx, store.Object{Key: keys[0], Body: []byte("again"), RetainUntil: time.Now().Add(time.Hour)})
			if !errors.Is(err, store.ErrExists) {
				t.Fatalf("a reused key was accepted: %v", err)
			}

			// The lock's own calls: a real answer on the locked bucket and the
			// sentinel on the unlocked one, never a request the bucket refuses
			// with a message about headers.
			err = s.SetLegalHold(ctx, keys[1], true)
			if tc.lock && err != nil {
				t.Fatalf("hold on a locked bucket: %v", err)
			}
			if !tc.lock && !errors.Is(err, store.ErrNotLockable) {
				t.Fatalf("hold on an unlocked bucket: want store.ErrNotLockable, got %v", err)
			}
			if !tc.lock {
				err = s.ExtendRetention(ctx, keys[1], time.Now().Add(48*time.Hour))
				if !errors.Is(err, store.ErrNotLockable) {
					t.Fatalf("extend on an unlocked bucket: want store.ErrNotLockable, got %v", err)
				}
			}
		})
	}
}
