// Package indextest is the one corpus every searcher is asked about.
//
// Three implementations of index.Searcher ship — memory, Postgres and a scan of
// the archive — and until this package existed each had its own test fixture
// and its own questions. That arrangement can only find the bugs someone
// thought to ask each of them about separately. It cannot find the one that
// matters most, which is two searchers answering the same question differently:
// a reader who moves a deployment from the scan to the index, or reads a
// replica while the primary is down, would see the trail change, and a trail
// that changes depending on who is asked is not evidence of anything.
//
// So the corpus is defined once, as records, and loaded into each searcher the
// way that searcher is fed in production — derived into rows for the indexers,
// written as archive objects for the scan. The expected answers are written out
// literally rather than computed, because an expectation derived from one
// implementation makes that implementation the definition and proves only that
// the others copy it.
package indextest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store"
)

// Profile is the one the corpus is written under.
const Profile = "security"

// Fields are the extension properties the corpus's catalogue indexes. Every
// searcher is given the same answer, so that a predicate on /credential_type
// means the same thing to all three.
var Fields = index.Fields{
	// One property of each kind the index has, so that a searcher which stores
	// or compares one of them differently from the others is caught. Three of
	// the four were invisible until this list held them: a kind nobody filters
	// on in a test is a kind nobody has checked.
	Filter: []string{"/credential_type", "/batch", "/expires_at", "/renewable"},
	Facet:  []string{"/credential_type"},
}

// FieldsOf is Fields in the shape the scanner asks for.
func FieldsOf(context.Context, *record.Record) (index.Fields, error) { return Fields, nil }

// Placed is one record and where the writer put it.
//
// The placement is part of the corpus rather than an implementation detail,
// because it is the answer to "where did this come from" that a reader checks
// against the digest chain, and the searchers have to agree about it too.
type Placed struct {
	Record *record.Record
	Tenant string
	// Object is the name within the day's prefix, and Line is which line of it,
	// counting from one.
	Object string
	Line   int
}

// Key is the archive key of the object this record is in.
func (p Placed) Key() string {
	day := p.Record.GetOccurredAt().AsTime().UTC()
	return fmt.Sprintf("profile=%s/tenant=%s/year=%s/month=%s/day=%s/%s",
		Profile, p.Tenant, day.Format("2006"), day.Format("01"), day.Format("02"), p.Object)
}

// The corpus's two days and two tenants. They are named here because the cases
// refer to them and a reader of a failure should not have to count back from a
// timestamp.
var (
	// Early is the older day, Late the newer one.
	Early = time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
	Late  = time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)
)

// Corpus is the records, in the order they were written.
//
// Nine records over two tenants, two days and two objects a day. Every
// occurred_at is distinct so that an ordering is total and a test failure names
// one row rather than a tie; recorded_at deliberately does not follow
// occurred_at, so that a searcher which sorts by the wrong one is caught rather
// than flattered.
func Corpus(t *testing.T) []Placed {
	t.Helper()
	// occurred, recorded-offset, tenant, action, failed, actor, credential, batch
	spec := []struct {
		occurred time.Time
		recorded time.Duration
		tenant   string
		object   string
		line     int
		action   string
		failed   bool
		actor    string
		kind     string
		credType string
		batch    int64
		// expires is the day part of an RFC 3339 value, which the index stores
		// as a time; renewable is stored as a boolean.
		expires   string
		renewable bool
	}{
		{Early.Add(0 * time.Minute), 5 * time.Minute, "initech", "a.ndjson.zst", 1,
			"wallet.credential.issued", false, "olga", "operator", "pid", 1,
			"2027-01-01T00:00:00Z", true},
		{Early.Add(1 * time.Minute), 1 * time.Minute, "initech", "a.ndjson.zst", 2,
			"wallet.credential.revoked", false, "olga", "operator", "pid", 1,
			"2027-01-01T00:00:00Z", false},
		{Early.Add(2 * time.Minute), 9 * time.Minute, "initech", "b.ndjson.zst", 1,
			"wallet.credential.issued", true, "ivan", "operator", "mdl", 2,
			"2027-01-01T00:00:00Z", true},
		{Early.Add(3 * time.Minute), 4 * time.Minute, "globex", "a.ndjson.zst", 1,
			"wallet.credential.issued", false, "svc-issuer", "service", "mdl", 2,
			"2027-01-01T00:00:00Z", false},
		{Late.Add(0 * time.Minute), 7 * time.Minute, "globex", "a.ndjson.zst", 1,
			"wallet.credential.revoked", false, "svc-issuer", "service", "pid", 3,
			"2027-06-01T00:00:00Z", true},
		{Late.Add(1 * time.Minute), 3 * time.Minute, "globex", "a.ndjson.zst", 2,
			"wallet.credential.issued", true, "olga", "operator", "pid", 3,
			"2027-06-01T00:00:00Z", false},
		{Late.Add(2 * time.Minute), 11 * time.Minute, "initech", "a.ndjson.zst", 1,
			"wallet.credential.issued", false, "ivan", "operator", "mdl", 4,
			"2027-06-01T00:00:00Z", true},
		{Late.Add(3 * time.Minute), 6 * time.Minute, "initech", "b.ndjson.zst", 1,
			"wallet.credential.revoked", false, "ivan", "operator", "pid", 4,
			"2027-06-01T00:00:00Z", false},
		{Late.Add(4 * time.Minute), 2 * time.Minute, "initech", "b.ndjson.zst", 2,
			"wallet.credential.issued", false, "olga", "operator", "mdl", 5,
			"2027-06-01T00:00:00Z", true},
	}

	out := make([]Placed, 0, len(spec))
	for n, s := range spec {
		data, err := structpb.NewStruct(map[string]any{
			"credential_type": s.credType,
			"batch":           float64(s.batch),
			"expires_at":      s.expires,
			"renewable":       s.renewable,
		})
		if err != nil {
			t.Fatal(err)
		}
		r := &record.Record{
			Id:               ID(n),
			SchemaVersion:    record.SchemaVersion,
			CatalogueVersion: "1.0.0",
			Source:           "wallet",
			TenantId:         s.tenant,
			Action:           s.action,
			Operation:        auditv1.Operation_OPERATION_CREATE,
			Outcome:          &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
			Actor:            &record.Actor{Kind: s.kind, Id: s.actor},
			Subject:          &record.Party{Kind: "person", Id: "holder-" + s.tenant},
			Targets:          []*record.Target{{Type: "credential", Id: fmt.Sprintf("cred-%d", n)}},
			Data:             data,
		}
		if s.failed {
			r.Outcome = &record.Outcome{Result: auditv1.Outcome_RESULT_FAILURE}
		}
		record.Assign(r)
		r.OccurredAt = timestamppb.New(s.occurred)
		r.RecordedAt = timestamppb.New(s.occurred.Add(s.recorded))
		out = append(out, Placed{Record: r, Tenant: s.tenant, Object: s.object, Line: s.line})
	}
	return out
}

// ID is the identifier of the nth record of the corpus.
//
// Every character is a hex digit, because one searcher stores identifiers in a
// uuid column and refuses anything else — which the other two happily accepted,
// so the first run of this suite against Postgres is what found it.
//
// It ends in a letter rather than a digit on purpose: an identifier ending in
// twelve digits reads as an account number to the repository's leak canary, and
// a fixture that trips it wastes somebody's afternoon.
func ID(n int) string {
	return fmt.Sprintf("018f0000-0000-7000-8000-000000000%02xa", n)
}

// Index loads the corpus into an indexer, as the writer's index sink does.
func Index(t *testing.T, idx index.Indexer, corpus []Placed) {
	t.Helper()
	rows := make([]index.Row, 0, len(corpus))
	for _, p := range corpus {
		rows = append(rows, index.RowOf(p.Record, index.ObjectAt{Key: p.Key(), Line: p.Line}, Fields))
	}
	if err := idx.Index(context.Background(), Profile, rows); err != nil {
		t.Fatal(err)
	}
}

// Archive writes the corpus into a store, as the writer's roller does: one
// object per key, zstd-compressed NDJSON, lines in order.
func Archive(t *testing.T, s store.Store, corpus []Placed) {
	t.Helper()
	// Group by key first: a record's Line is its position in its object, so the
	// object cannot be written until every line of it is known.
	order := make([]string, 0, len(corpus))
	lines := map[string][]Placed{}
	for _, p := range corpus {
		key := p.Key()
		if _, seen := lines[key]; !seen {
			order = append(order, key)
		}
		lines[key] = append(lines[key], p)
	}
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = encoder.Close() }()
	for _, key := range order {
		var body []byte
		for want := 1; want <= len(lines[key]); want++ {
			var found *record.Record
			for _, p := range lines[key] {
				if p.Line == want {
					found = p.Record
				}
			}
			if found == nil {
				t.Fatalf("indextest: %s has no line %d; the corpus is inconsistent", key, want)
			}
			line, err := record.Canonical(found)
			if err != nil {
				t.Fatal(err)
			}
			body = append(append(body, line...), '\n')
		}
		if err := s.Put(context.Background(), store.Object{
			Key: key, Body: encoder.EncodeAll(body, nil),
			RetainUntil: time.Now().AddDate(1, 0, 0).UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
}
