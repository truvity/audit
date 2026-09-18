package writer_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/proto"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/pgtest"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store/storetest"
)

// pgParts builds the writer's index and deduplication store on a real
// database. Each replica gets its own pair, as a deployment's would: what they
// share is the database, not the objects in front of it.
func pgParts(t *testing.T, pool *pgxpool.Pool, instance string, s *storetest.Memory) parts {
	t.Helper()
	indexer, err := postgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	dedupe, err := postgres.NewDedupe(pool, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return parts{store: s, indexer: indexer, dedupe: dedupe, instance: instance}
}

// lineAt reads one line of an archive object, which is what a row's object key
// and line number address.
func lineAt(t *testing.T, s *storetest.Memory, key string, line int) *record.Record {
	t.Helper()
	body, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	plain, err := decoder.DecodeAll(body, nil)
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	lines := strings.Split(strings.TrimRight(string(plain), "\n"), "\n")
	if line < 1 || line > len(lines) {
		t.Fatalf("%s has %d lines and a row addresses line %d", key, len(lines), line)
	}
	var r record.Record
	if err := record.Unmarshal([]byte(lines[line-1]), &r); err != nil {
		t.Fatalf("%s:%d: %v", key, line, err)
	}
	return &r
}

// The writer end to end against a real database: objects in the archive, rows
// and counts in Postgres, and every row addressing the line it came from.
func TestTheWriterIndexesIntoPostgres(t *testing.T) {
	pool := pgtest.Open(t)
	b := buildWith(t, pgParts(t, pool, "writer-1", nil))
	ctx := context.Background()

	ids := map[string]bool{}
	for i := 0; i < 3; i++ {
		r := fresh(t)
		write(t, b, r)
		ids[r.GetId()] = true
	}

	var events, contexts, data int
	for _, q := range []struct {
		into *int
		sql  string
	}{
		{&events, `select count(*) from events_core where profile = 'security'`},
		{&contexts, `select count(*) from events_context where profile = 'security'`},
		{&data, `select count(*) from events_data where profile = 'security'`},
	} {
		if err := pool.QueryRow(ctx, q.sql).Scan(q.into); err != nil {
			t.Fatal(err)
		}
	}
	if events != 3 || contexts != 3 {
		t.Fatalf("%d events and %d context rows, want 3 and 3", events, contexts)
	}
	if data == 0 {
		t.Fatal("the catalogue marks properties filterable and none were indexed")
	}

	var total int64
	err := pool.QueryRow(ctx, `
		select total from facet_counts
		 where profile = 'security' and field = $1 and facet_value = $2`,
		index.FieldAction, "wallet.credential.issued").Scan(&total)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("counted %d, want 3", total)
	}

	// Provenance is the point of keeping the key and the line: an answer has to
	// be checkable against the copy the digest chain accounts for.
	rows, err := pool.Query(ctx, `select id, object_key, line from events_core where profile = 'security'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var id, key string
		var line int
		if err := rows.Scan(&id, &key, &line); err != nil {
			t.Fatal(err)
		}
		if !ids[id] {
			t.Fatalf("the index holds %s, which was never written", id)
		}
		if got := lineAt(t, b.store, key, line); got.GetId() != id {
			t.Fatalf("row %s addresses %s:%d, which holds %s", id, key, line, got.GetId())
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != 3 {
		t.Fatalf("checked %d rows, want 3", seen)
	}
}

// The reason the table is shared: a redelivery landing on either replica is
// absorbed by whichever gets it, not only by the one that saw the original.
func TestTwoWritersShareOneDedupeTable(t *testing.T) {
	pool := pgtest.Open(t)
	archive := storetest.NewMemory()
	first := buildWith(t, pgParts(t, pool, "writer-1", archive))
	second := buildWith(t, pgParts(t, pool, "writer-2", archive))
	ctx := context.Background()

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

	var events int
	if err := pool.QueryRow(ctx,
		`select count(*) from events_core where profile = 'security'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("%d rows for two records, want 2", events)
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

// An addendum found through the real index: the row Postgres keeps for the
// issuance is what names the object whose lock is lengthened.
func TestARenewalFindsTheIssuanceThroughPostgres(t *testing.T) {
	pool := pgtest.Open(t)
	pg, err := postgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	renewalExtends(t, buildExtendingOn(t, pg, pg))
}
