package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/index/postgres"
	"github.com/truvity/audit/internal/pgtest"
)

func at(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.UTC()
}

func row(t *testing.T, id string, recorded time.Time) index.Row {
	t.Helper()
	return index.Row{
		ID: id, TenantID: "acme",
		OccurredAt: recorded.Add(-time.Minute), RecordedAt: recorded,
		Source: "wallet", Action: "wallet.credential.issued",
		Operation: "create", Outcome: "success",
		ActorKind: "operator", ActorID: "ps_abc",
		SubjectKind: "person", SubjectID: "ps_def",
		ClientAddress: "203.0.113.9", RequestID: "req-1", TraceID: "trace-1",
		ObserverID:  "workload:wallet",
		TargetTypes: []string{"credential"}, TargetIDs: []string{"cred-1"},
		ObjectKey: "profile=security/x.ndjson.zst", Line: 1,
		Data: []index.Value{{Path: "/credential_type", Kind: index.Text, Text: "pid"}},
	}
}

// The migration is applied by a job, and a job runs twice more often than
// anybody plans for.
func TestMigrateIsIdempotent(t *testing.T) {
	pool := pgtest.Open(t)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("applying the schema twice: %v", err)
	}
	if err := postgres.CheckVersion(ctx, pool); err != nil {
		t.Fatal(err)
	}
}

// A writer whose database is at another version refuses to start, and the
// refusal has to say what to do about it.
func TestCheckVersionSaysWhatToRun(t *testing.T) {
	pool := pgtest.Open(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `insert into audit_schema_version (version) values (99)`); err != nil {
		t.Fatal(err)
	}
	err := postgres.CheckVersion(ctx, pool)
	if err == nil {
		t.Fatal("a database at another schema version must not be accepted")
	}
	if !strings.Contains(err.Error(), "audit migrate") {
		t.Errorf("the refusal should say what to run: %v", err)
	}
}

// This is the reason counting is not a call of its own. A re-delivered record
// must leave the counts where they were, and only the transaction that inserted
// the row can tell a repeat from a new record.
func TestIndexingTheSameCopyTwiceCountsItOnce(t *testing.T) {
	pool := pgtest.Open(t)
	ctx := context.Background()
	i, err := postgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	rows := []index.Row{row(t, "018f0000-0000-7000-8000-00000000000a", at(t, "2026-09-17T10:17:00Z"))}
	for n := 0; n < 3; n++ {
		if err := i.Index(ctx, "security", rows); err != nil {
			t.Fatalf("pass %d: %v", n, err)
		}
	}

	var events, data int
	if err := pool.QueryRow(ctx, `select count(*) from events_core`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from events_data`).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if events != 1 || data != 1 {
		t.Fatalf("%d events and %d data rows, want 1 and 1", events, data)
	}

	var total int64
	err = pool.QueryRow(ctx, `
		select total from facet_counts
		 where field = $1 and facet_value = $2`,
		index.FieldAction, "wallet.credential.issued").Scan(&total)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("one record counted %d times", total)
	}
}

// A batch spanning a month boundary needs two partitions, and a writer running
// at midnight on the first has nobody awake to create them.
func TestPartitionsAreCreatedForTheRowsThatNeedThem(t *testing.T) {
	pool := pgtest.Open(t)
	ctx := context.Background()
	i, err := postgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	rows := []index.Row{
		row(t, "018f0000-0000-7000-8000-00000000000a", at(t, "2026-09-30T23:59:00Z")),
		row(t, "018f0000-0000-7000-8000-00000000000b", at(t, "2026-10-01T00:01:00Z")),
	}
	if err := i.Index(ctx, "security", rows); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"2026_09", "2026_10"} {
		var found bool
		err := pool.QueryRow(ctx, `select to_regclass($1) is not null`, "events_core_"+suffix).Scan(&found)
		if err != nil || !found {
			t.Fatalf("partition events_core_%s was not created (%v)", suffix, err)
		}
	}
	var events int
	if err := pool.QueryRow(ctx, `select count(*) from events_core`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("%d events across the boundary, want 2", events)
	}
}

// A profile keeps what happened for longer than it keeps who it happened to.
func TestPurgeIdentifyingKeepsTheEvent(t *testing.T) {
	pool := pgtest.Open(t)
	ctx := context.Background()
	i, err := postgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	id := "018f0000-0000-7000-8000-00000000000a"
	if err := i.Index(ctx, "security", []index.Row{row(t, id, at(t, "2026-09-17T10:17:00Z"))}); err != nil {
		t.Fatal(err)
	}
	if err := i.Purge(ctx, "security", at(t, "2026-10-01T00:00:00Z"), index.Identifying); err != nil {
		t.Fatal(err)
	}

	var actorKind, actorID, address string
	err = pool.QueryRow(ctx,
		`select actor_kind, actor_id, client_address from events_context where id = $1`, id).
		Scan(&actorKind, &actorID, &address)
	if err != nil {
		t.Fatalf("purging the identifying columns removed the event: %v", err)
	}
	if actorID != "" || address != "" {
		t.Fatalf("identifying columns survived: actor %q address %q", actorID, address)
	}
	if actorKind != "operator" {
		t.Fatal("what kind of actor it was is not identifying and must survive")
	}

	var events int
	if err := pool.QueryRow(ctx, `select count(*) from events_core`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatal("purging who it happened to must not remove what happened")
	}
}

// When the retention itself expires, everything goes, counts included.
func TestPurgeEverythingLeavesNothing(t *testing.T) {
	pool := pgtest.Open(t)
	ctx := context.Background()
	i, err := postgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := i.Index(ctx, "security", []index.Row{
		row(t, "018f0000-0000-7000-8000-00000000000a", at(t, "2026-09-17T10:17:00Z")),
	}); err != nil {
		t.Fatal(err)
	}
	if err := i.Purge(ctx, "security", at(t, "2026-10-01T00:00:00Z"), index.Everything); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"events_core", "events_context", "events_data", "facet_counts"} {
		var n int
		if err := pool.QueryRow(ctx, fmt.Sprintf(`select count(*) from %s`, table)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s kept %d rows past the profile's retention", table, n)
		}
	}
}

// Asking must not mark. If it did, a crash between the question and the put
// would leave the identifier remembered and the record nowhere, and the
// redelivery that would have saved it would look like a repeat.
func TestAskingDoesNotMark(t *testing.T) {
	pool := pgtest.Open(t)
	ctx := context.Background()
	d, err := postgres.NewDedupe(pool, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"a", "b"}
	if seen, err := d.Seen(ctx, ids); err != nil || len(seen) != 0 {
		t.Fatalf("nothing has been written yet: %v %v", seen, err)
	}
	if seen, err := d.Seen(ctx, ids); err != nil || len(seen) != 0 {
		t.Fatalf("asking twice must still report nothing written: %v %v", seen, err)
	}

	if err := d.Mark(ctx, ids); err != nil {
		t.Fatal(err)
	}
	seen, err := d.Seen(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if !seen["a"] || !seen["b"] {
		t.Fatalf("marked records are not reported as written: %v", seen)
	}
}

// A batch carrying a redelivery beside its original is an ordinary shape, and
// an upsert of one key twice in one statement is an error in Postgres.
func TestMarkingARepeatWithinOneBatch(t *testing.T) {
	pool := pgtest.Open(t)
	ctx := context.Background()
	d, err := postgres.NewDedupe(pool, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Mark(ctx, []string{"a", "a", "", "b"}); err != nil {
		t.Fatalf("a repeated identifier in one batch: %v", err)
	}
	seen, err := d.Seen(ctx, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("%v, want both marked once", seen)
	}
}

// Past the window an identifier is forgotten, which is what bounds the table.
func TestTheWindowForgets(t *testing.T) {
	pool := pgtest.Open(t)
	ctx := context.Background()
	now := at(t, "2026-09-17T10:00:00Z")
	d, err := postgres.NewDedupe(pool, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	d.Now = func() time.Time { return now }
	if err := d.Mark(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Hour)
	seen, err := d.Seen(ctx, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if seen["a"] {
		t.Fatal("an identifier past the window is still remembered")
	}

	if err := d.Purge(ctx, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from seen`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d identifiers survived the purge", n)
	}
}
