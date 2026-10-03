package writer_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/pgtest"
	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/store/storetest"
)

// pgParts builds the writer's deduplication store on a real database. Each replica gets its own pair, as a deployment's would: what they
// share is the database, not the objects in front of it.
func pgParts(t *testing.T, pool *pgxpool.Pool, instance string, s *storetest.Memory) parts {
	t.Helper()
	dedupe, err := postgres.NewDedupe(pool, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return parts{store: s, dedupe: dedupe, instance: instance}
}

// lineAt reads one line of an archive object, which is what a row's object key
// and line number address.
func lineAt(t *testing.T, s *storetest.Memory, key string, line int) *record.Record {
	t.Helper()
	records := objectRecords(t, s, key)
	if line < 1 || line > len(records) {
		t.Fatalf("%s has %d lines and a row addresses line %d", key, len(records), line)
	}
	return records[line-1]
}

// The reason the table is shared: a redelivery landing on either replica is
// absorbed by whichever gets it, not only by the one that saw the original.
func TestTwoWritersShareOneDedupeTable(t *testing.T) {
	pool := pgtest.Open(t)
	archive := storetest.NewMemory()
	first := buildWith(t, pgParts(t, pool, "writer-1", archive))
	second := buildWith(t, pgParts(t, pool, "writer-2", archive))

	// The redelivery is a copy of what was published, not the record the first
	// writer has already stamped.
	original := fresh(t)
	redelivered := proto.Clone(original).(*record.Record)
	write(t, first, original)
	write(t, second, redelivered)
	if second.duplicates != 1 {
		t.Fatalf("the second replica took a record the first had written (%d duplicates)", second.duplicates)
	}

	// And the other way round, because neither replica is the special one.
	other := fresh(t)
	otherAgain := proto.Clone(other).(*record.Record)
	write(t, second, other)
	write(t, first, otherAgain)
	if first.duplicates != 1 {
		t.Fatalf("the first replica took a record the second had written (%d duplicates)", first.duplicates)
	}

	copies := 0
	for _, c := range decode(t, archive) {
		if c.GetProfile() == "security" {
			copies++
		}
	}
	if copies != 2 {
		t.Fatalf("%d copies for two records, want 2", copies)
	}
}

// The contrast, and the reason the writer refuses to start more than one
// replica without a shared store: in process, a replica knows only what it has
// seen itself, and the redelivery is written a second time.
func TestWithoutASharedTableAReplicaDoesNotAbsorb(t *testing.T) {
	archive := storetest.NewMemory()
	first := buildWith(t, parts{store: archive, instance: "writer-1"})
	second := buildWith(t, parts{store: archive, instance: "writer-2"})

	original := fresh(t)
	redelivered := proto.Clone(original).(*record.Record)
	write(t, first, original)
	write(t, second, redelivered)

	if second.duplicates != 0 {
		t.Fatal("an in-process table cannot know what another replica wrote")
	}
	copies := 0
	for _, c := range decode(t, archive) {
		if c.GetProfile() == "security" && c.GetId() == original.GetId() {
			copies++
		}
	}
	if copies != 2 {
		t.Fatalf("%d copies of one record, want the 2 that make this unsafe", copies)
	}
}
