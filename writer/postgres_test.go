package writer_test

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/internal/pgtest"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store/storetest"
	"github.com/truvity/audit/writer"
)

// With a database, replicas share one deduplication table and one index, and
// a replica that brings a key directory other than the deployment's is refused:
// it would give the same person a second pseudonym.
func TestReplicasShareTheDatabaseAndItsKeyDirectory(t *testing.T) {
	pool := pgtest.Open(t)
	ctx := context.Background()
	archive := storetest.NewMemory()
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	open := func(instance, keyDir string) (*writer.Writer, error) {
		provider, err := keys.NewLocal(root, keyDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = provider.Close() })
		return writer.Open(ctx, writer.Config{
			Archive: archive, Profiles: profiles(t), Keys: provider,
			Database: pool, Replicas: 2, Instance: instance, Self: "workload:test",
		})
	}

	one, err := open("writer-1", dir)
	if err != nil {
		t.Fatalf("the first replica: %v", err)
	}
	two, err := open("writer-2", dir)
	if err != nil {
		t.Fatalf("a second replica on the same directory: %v", err)
	}
	if _, err := open("writer-3", t.TempDir()); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("a replica with its own key directory was let in: %v", err)
	}

	r := &record.Record{
		Id:               record.NewID(),
		Source:           "audit",
		CatalogueVersion: "1.0.0",
		SchemaVersion:    record.SchemaVersion,
		Action:           "audit.writer.started",
		Operation:        auditv1.Operation_OPERATION_CREATE,
		TenantId:         record.TenantPlatform,
		Actor:            &record.Actor{Kind: "system", Id: "test"},
		Outcome:          &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
		Observer:         &record.Observer{Version: "1", Instance: "test"},
	}
	r.OccurredAt = timestamppb.Now()
	for _, w := range []*writer.Writer{one, two} {
		if _, err := w.Write(ctx, &sink.Request{Records: []*record.Record{r}, Delivery: sink.Block}); err != nil {
			t.Fatal(err)
		}
	}
	var copies int
	if err := pool.QueryRow(ctx, `select count(*) from events_core where id = $1`, r.GetId()).Scan(&copies); err != nil {
		t.Fatal(err)
	}
	if copies != 1 {
		t.Fatalf("the same record through two replicas is indexed %d times, want once", copies)
	}
	for _, w := range []*writer.Writer{one, two} {
		if err := w.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
}
