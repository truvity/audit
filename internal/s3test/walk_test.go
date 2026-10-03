package s3test_test

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/audit/internal/s3test"
	"github.com/truvity/audit/internal/ulid"
	"github.com/truvity/audit/store"
)

// This is the test that would have caught both archive-walk bugs. S3 answers a
// thousand keys at a time whatever is asked, and the tenant sits between the
// profile and the date; a memory store hides both facts.

var hour = time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

// A listing asked for everything must return everything, across as many pages
// as S3 chooses to use.
func TestListReturnsEverythingPastOnePage(t *testing.T) {
	s := s3test.Open(t, false)
	const n = 1100
	s3test.Fill(t, s, "security", "acme", hour, n)

	entries, err := s.List(context.Background(), "records/security/", "", 0)
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
	keys := s3test.Fill(t, s, "security", "acme", hour, 10)

	entries, err := s.List(context.Background(), "records/security/", keys[3], 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Key != keys[4] {
		t.Fatalf("got %d entries starting %v, want 2 from %s", len(entries), entries, keys[4])
	}
}

// The tenants under a profile, without walking the objects beneath them. This
// is what a walk asks before it lists hours, and getting it wrong made a walk
// cover one tenant.
func TestPrefixesListsEveryTenant(t *testing.T) {
	s := s3test.Open(t, false)
	for _, tenant := range []string{"acme", "globex", "initech"} {
		s3test.Fill(t, s, "security", tenant, hour, 3)
	}
	s3test.Fill(t, s, "history", "acme", hour, 3)

	groups, err := s.Prefixes(context.Background(), "records/security/", "/")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"records/security/acme/",
		"records/security/globex/",
		"records/security/initech/",
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

// WalkHours is what every archive reader uses. It must reach every object of
// the hours asked for, however many pages that takes, in key order, and stop
// at the end of the range.
func TestWalkHoursReachesEveryObjectOfTheRangeAndNoMore(t *testing.T) {
	s := s3test.Open(t, false)
	s3test.Fill(t, s, "security", "acme", hour, 1100)
	s3test.Fill(t, s, "security", "acme", hour.Add(time.Hour), 4)
	s3test.Fill(t, s, "security", "acme", hour.Add(2*time.Hour), 3)
	s3test.Fill(t, s, "security", "acme", hour.Add(-time.Hour), 2)

	var keys []string
	err := store.WalkHours(context.Background(), s, "security", "acme", hour, hour.Add(time.Hour), "",
		func(e store.Entry) error {
			keys = append(keys, e.Key)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1104 {
		t.Fatalf("the walk saw %d of 1104 objects in two hours", len(keys))
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] <= keys[i-1] {
			t.Fatalf("keys came back out of order at %d", i)
		}
	}

	// Resumed after a key, it continues from the next one: the cursor a
	// follower keeps.
	var rest int
	err = store.WalkHours(context.Background(), s, "security", "acme", hour, hour.Add(time.Hour), keys[1000],
		func(store.Entry) error { rest++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if rest != 103 {
		t.Fatalf("resumed after the 1001st key, saw %d, want 103", rest)
	}
}

// Every tenant of a profile is reached, tenant by tenant.
func TestWalkProfileReachesEveryTenant(t *testing.T) {
	s := s3test.Open(t, false)
	s3test.Fill(t, s, "security", "acme", hour, 7)
	s3test.Fill(t, s, "security", "globex", hour, 5)
	s3test.Fill(t, s, "history", "acme", hour, 2)

	seen := map[string]int{}
	err := store.WalkProfile(context.Background(), s, "security", hour, hour,
		func(tenant string, _ store.Entry) error {
			seen[tenant]++
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if seen["acme"] != 7 || seen["globex"] != 5 || len(seen) != 2 {
		t.Fatalf("the walk saw %v", seen)
	}
}

// The writer's real path names a lock mode on every put, and a bucket with
// Object Lock takes it. What is NOT asserted is that the lock holds: LocalStack
// accepts the parameters without enforcing compliance retention, so a test
// claiming the object cannot be deleted would pass for the wrong reason.
func TestALockedPutIsAccepted(t *testing.T) {
	s := s3test.Open(t, true)
	ctx := context.Background()
	key := store.RecordKey("security", "acme", hour, ulid.From(hour, 1))
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
	// adding a version nothing accounts for.
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
