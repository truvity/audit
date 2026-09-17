package writer_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store/storetest"
)

func archive(t *testing.T) (*writer.SchemaArchive, *storetest.Memory) {
	t.Helper()
	s := storetest.NewMemory()
	at := day(t, "2026-09-17T10:30:00Z")
	return &writer.SchemaArchive{
		Store: s, Now: func() time.Time { return at },
		RetainUntil: func(now time.Time) time.Time { return now.AddDate(7, 0, 0) },
	}, s
}

// A locked object outlives the deployment that wrote it. What sits beside it
// has to be enough on its own.
func TestArchiveKeepsWhatMakesRecordsReadable(t *testing.T) {
	a, s := archive(t)
	c, err := catalogue.Load([]byte(walletDoc), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := a.EnsureCatalogue(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureRecord(ctx, record.SchemaVersion); err != nil {
		t.Fatal(err)
	}

	keys := s.Keys()
	for _, want := range []string{
		"schema/wallet/1.0.0/catalogue.yaml",
		"schema/audit/v1/record.v1.schema.json",
		"schema/audit/v1/record.proto",
	} {
		if !has(keys, want) {
			t.Errorf("the archive lacks %s\nheld: %v", want, keys)
		}
	}
	// The extension schema is there under a name of its own.
	var found bool
	for _, k := range keys {
		if strings.HasPrefix(k, "schema/wallet/1.0.0/") && strings.HasSuffix(k, ".json") {
			found = true
		}
	}
	if !found {
		t.Errorf("the extension schema was not archived: %v", keys)
	}

	// The catalogue is kept as it was registered, not as it would be written
	// back: a re-serialised catalogue is not the one that was registered.
	body, err := s.Get(ctx, "schema/wallet/1.0.0/catalogue.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != walletDoc {
		t.Fatal("the archived catalogue is not the document that was registered")
	}

	// A record whose schema has been deleted is a record nobody can read.
	o, _ := s.Object("schema/audit/v1/record.proto")
	if o.RetainUntil.Year() != 2033 {
		t.Fatalf("the proto is locked until %s, which must outlive the records that name it", o.RetainUntil)
	}
}

// The published schema carries the proto's comments, which is why it is the
// copy the archive keeps.
func TestArchivedSchemaCarriesMeaning(t *testing.T) {
	a, s := archive(t)
	if err := a.EnsureRecord(context.Background(), record.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	body, err := s.Get(context.Background(), "schema/audit/v1/record.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Record is one thing that happened") {
		t.Fatal("the archived schema carries no descriptions; a reader would get shape without meaning")
	}
}

// A catalogue version is immutable by construction, so a key already taken is
// the right answer and not a collision.
func TestArchiveIsIdempotentAcrossWriters(t *testing.T) {
	a, s := archive(t)
	c, err := catalogue.Load([]byte(walletDoc), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := a.EnsureCatalogue(ctx, c); err != nil {
		t.Fatal(err)
	}
	before := s.Len()

	// A second writer, which has archived nothing itself, finds the keys taken.
	second := &writer.SchemaArchive{Store: s, Now: a.Now, RetainUntil: a.RetainUntil}
	if err := second.EnsureCatalogue(ctx, c); err != nil {
		t.Fatalf("a taken key must be the right answer, not a failure: %v", err)
	}
	if s.Len() != before {
		t.Fatalf("the second writer wrote again: %d objects, was %d", s.Len(), before)
	}
}

// Doing this after the records would leave a window in which the archive holds
// records nothing explains.
func TestTheWriterArchivesBeforeTheFirstRecordLands(t *testing.T) {
	b := build(t)
	a, _ := archive(t)
	a.Store = b.store
	b.writer.Archive = a

	write(t, b, fresh(t))

	keys := b.store.Keys()
	if !has(keys, "schema/wallet/1.0.0/catalogue.yaml") {
		t.Fatalf("the catalogue was not archived: %v", keys)
	}
	if !has(keys, "schema/audit/v1/record.proto") {
		t.Fatalf("the proto was not archived: %v", keys)
	}
}

func has(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}
