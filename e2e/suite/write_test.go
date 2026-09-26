// This file proves the write path end to end: an emitter, exactly as an
// application would embed it, hands a record to the writer's Service over
// HTTP; in `mode: stream` that receiver publishes it to JetStream; a
// consumer Deployment on the other end takes it from the stream and puts
// it in the archive AND the Postgres index. Nothing here calls the writer
// in process or reaches into a Pod — every hop crosses a real Service, the
// same claim truvity/policy's own example suite proves for its chart.
package suite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"math/big"
	"net/http"
	"testing"
	"time"

	auditv1 "github.com/truvity/audit/gen/audit/v1"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/emit"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// writerPort is the writer's one Service port in every mode — direct and
// stream keep the same address on purpose (charts/audit/templates/writer.yaml).
const writerPort = 8080

// randomTenant draws a tenant id unique to one test run, so two runs
// against the SAME standing install (this suite is not torn down between
// runs) never collide in the index.
func randomTenant(t *testing.T) string {
	t.Helper()
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, 12)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			t.Fatalf("draw a random tenant suffix: %v", err)
		}
		out[i] = alphabet[n.Int64()]
	}
	return "e2e-suite-" + string(out)
}

// writerEmitter builds an emitter that speaks for the common catalogue's
// OWN source ("audit" — catalogue/common.yaml) against the writer Service.
// The common catalogue is built into the writer (main.go: `Builtin:
// []*catalogue.Catalogue{common}`), so it needs no registration step here —
// unlike an application's own catalogue, which would RegisterCatalogue
// first.
func writerEmitter(ctx context.Context, t *testing.T) *emit.Emitter {
	t.Helper()

	baseURL, err := shared.cluster.ServiceURL(ctx, shared.names.Namespace, shared.names.Release, writerPort)
	if err != nil {
		t.Fatalf("resolve the writer Service: %v", err)
	}

	common, err := catalogue.Common()
	if err != nil {
		t.Fatalf("load the common catalogue: %v", err)
	}

	e, err := emit.New(emit.Options{
		Source:    common.Source,
		Catalogue: common,
		Sink:      sink.NewClient(&http.Client{Timeout: 10 * time.Second}, baseURL),
		Timeout:   15 * time.Second,
	})
	if err != nil {
		t.Fatalf("build the emitter: %v", err)
	}
	return e
}

// TestRecordIsWrittenAndIndexed is the core claim: an event this suite
// emits over the writer's Service reaches the archive AND the query role's
// own view of the index — the two things a query service would answer
// from. `audit.search` is `delivery: async` (catalogue/common.yaml), so
// this polls rather than asserting once; in `mode: stream` the record also
// crosses JetStream before a consumer Deployment ever sees it.
//
// It reads back through the QUERY role, not the writer's — proving, in the
// same call, that migrate's `--reader` grant actually lets that role select
// what the writer role wrote (row-level security binds only a role that
// does not own the tables; see index/postgres/postgres.go's GrantReader).
func TestRecordIsWrittenAndIndexed(t *testing.T) {
	// The consumer only rolls what it gathered from the stream every
	// roll.interval (30s, charts/audit/testdata/values/e2e.yaml's chart
	// default) or 5000 records, whichever comes first — so a record
	// submitted just after a roll can take most of the next one to reach
	// the archive and the index. 2 minutes leaves margin over that.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	tenant := randomTenant(t)
	e := writerEmitter(ctx, t)
	defer func() { _ = e.Close() }()

	r := &record.Record{
		Action:    "audit.search",
		Operation: auditv1.Operation_OPERATION_ACCESS,
		TenantId:  tenant,
		Actor:     &record.Actor{Kind: "service", Id: "e2e-suite"},
		Targets:   []*record.Target{{Type: "profile", Id: "security"}},
		Outcome:   &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
		Context:   &record.Context{ClientAddresses: []string{"203.0.113.7"}},
	}
	if err := e.Record(ctx, r); err != nil {
		t.Fatalf("emit audit.search: %v", err)
	}

	readerDB := openDSN(ctx, t, shared.queryDSN)
	defer func() { _ = readerDB.Close() }()

	// events_core and events_context both enable row-level security
	// (index/postgres/schema/0001_index.sql, tightened by 0004_tenant_list.sql):
	// a reader that sets nothing sees NOTHING, on purpose — "unset" used to
	// read as "every tenant", which was the exact mistake the second
	// policy exists to catch. A real query service sets `audit.tenant_ids`
	// from the caller's own grant before every query; this test does the
	// same, on one held connection, since `SET` is per-session and a
	// `*sql.DB`'s pool would otherwise hand the next query a different one.
	conn, err := readerDB.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire a connection: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `select set_config('audit.tenant_ids', $1, false)`,
		fmt.Sprintf(`["%s"]`, tenant)); err != nil {
		t.Fatalf("set audit.tenant_ids: %v", err)
	}

	eventually(t, 100*time.Second, func() error {
		var action, actorID string
		err := conn.QueryRowContext(ctx,
			`select action, actor_id from events_core join events_context using (profile, id, recorded_at)
			 where tenant_id = $1 and action = 'audit.search'`,
			tenant,
		).Scan(&action, &actorID)
		if err == sql.ErrNoRows {
			return fmt.Errorf("no row for tenant %s yet", tenant)
		}
		if err != nil {
			return err
		}
		if actorID != "e2e-suite" {
			return fmt.Errorf("indexed actor_id = %q, want e2e-suite", actorID)
		}
		return nil
	})
}
