package s3test_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/internal/s3test"
	"github.com/truvity/audit/store"
)

// This is the test that would have caught both archive-walk bugs. S3 answers a
// thousand keys at a time whatever is asked, and the tenant sits between the
// profile and the date; a memory store hides both facts.

const day = "year=2026/month=09/day=17"

// A listing asked for everything must return everything, across as many pages
// as S3 chooses to use.
func TestListReturnsEverythingPastOnePage(t *testing.T) {
	s := s3test.Open(t, false)
	const n = 1100
	s3test.Fill(t, s, "security", "acme", day, n)

	entries, err := s.List(context.Background(), "profile=security/", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != n {
		t.Fatalf("listed %d of %d objects: a page was taken for the whole", len(entries), n)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].Key <= entries[i-1].Key {
			t.Fatalf("keys came back out of order at %d", i)
		}
	}
}

// With a limit the caller is paging, and gets one page from after its key.
func TestListWithALimitPagesFromAKey(t *testing.T) {
	s := s3test.Open(t, false)
	keys := s3test.Fill(t, s, "security", "acme", day, 10)

	entries, err := s.List(context.Background(), "profile=security/", keys[3], 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Key != keys[4] {
		t.Fatalf("got %d entries starting %v, want 2 from %s", len(entries), entries, keys[4])
	}
}

// The tenants under a profile, without walking the objects beneath them. This
// is what the digest builder asks before it walks days, and getting it wrong
// made a digest cover one tenant.
func TestPrefixesListsEveryTenant(t *testing.T) {
	s := s3test.Open(t, false)
	for _, tenant := range []string{"acme", "globex", "initech"} {
		s3test.Fill(t, s, "security", tenant, day, 3)
	}
	s3test.Fill(t, s, "history", "acme", day, 3)

	groups, err := s.Prefixes(context.Background(), "profile=security/", "/")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"profile=security/tenant=acme/",
		"profile=security/tenant=globex/",
		"profile=security/tenant=initech/",
	}
	if len(groups) != len(want) {
		t.Fatalf("got %v, want %v", groups, want)
	}
	for i := range want {
		if groups[i] != want[i] {
			t.Fatalf("got %v, want %v", groups, want)
		}
	}
}

// WalkDays is what every archive job uses. It must reach every tenant's day and
// every object of it, however many pages that takes.
func TestWalkDaysReachesEveryTenantAndEveryObject(t *testing.T) {
	s := s3test.Open(t, false)
	s3test.Fill(t, s, "security", "acme", day, 1100)
	s3test.Fill(t, s, "security", "globex", day, 5)
	s3test.Fill(t, s, "security", "acme", "year=2026/month=09/day=18", 4)

	at, err := time.Parse("2006-01-02", "2026-09-17")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	err = store.WalkDays(context.Background(), s, "profile=security", at, at, func(e store.Entry) error {
		switch {
		case strings.Contains(e.Key, "tenant=acme"):
			seen["acme"]++
		case strings.Contains(e.Key, "tenant=globex"):
			seen["globex"]++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen["acme"] != 1100 {
		t.Fatalf("the walk saw %d of 1100 objects of the first tenant", seen["acme"])
	}
	if seen["globex"] != 5 {
		t.Fatalf("the walk saw %d of 5 objects of the second tenant", seen["globex"])
	}
}

// The writer's real path names a lock mode on every put, and a bucket with
// Object Lock takes it. What is NOT asserted is that the lock holds: LocalStack
// accepts the parameters without enforcing compliance retention, so a test
// claiming the object cannot be deleted would pass for the wrong reason.
func TestALockedPutIsAccepted(t *testing.T) {
	s := s3test.Open(t, true)
	ctx := context.Background()
	key := "profile=security/tenant=acme/" + day + "/a.ndjson.zst"
	if err := s.Put(ctx, store.Object{
		Key: key, Body: []byte("{}"),
		RetainUntil: time.Now().Add(24 * time.Hour).UTC(),
	}); err != nil {
		t.Fatalf("a locked put was refused: %v", err)
	}
	entry, err := s.Head(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if entry.RetainUntil.IsZero() {
		t.Fatal("the retention did not reach the object")
	}

	// And the key cannot be taken twice, which is what keeps a writer from
	// adding a version no digest accounts for.
	err = s.Put(ctx, store.Object{Key: key, Body: []byte("{}"), RetainUntil: entry.RetainUntil})
	if err == nil {
		t.Fatal("a key was written twice")
	}
}

// An export bucket has no Object Lock, and a put naming a lock mode to such a
// bucket is refused by S3 outright. This is the shape the export path uses.
func TestAnUnlockedBucketTakesAnUnlockedPut(t *testing.T) {
	s := s3test.Open(t, false)
	if err := s.Put(context.Background(), store.Object{
		Key: "export/j1/records.ndjson", Body: []byte("{}"),
	}); err != nil {
		t.Fatalf("an unlocked put to an unlocked bucket was refused: %v", err)
	}
}
