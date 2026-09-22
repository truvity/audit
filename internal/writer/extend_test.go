package writer_test

import (
	"context"
	"crypto/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/audit/catalogue"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/storetest"
)

const issuerSchema = `{
  "$id": "https://schemas.example/issuer/credential.json",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "valid_until": {
      "type": "string",
      "format": "date-time",
      "x-audit-class": "evidence",
      "x-audit-pii": "none",
      "x-audit-expiry": true
    },
    "renews": {
      "type": "array",
      "items": {"type": "string", "x-audit-class": "evidence", "x-audit-pii": "none"},
      "x-audit-class": "evidence",
      "x-audit-pii": "none"
    }
  }
}`

const issuerDoc = `
source: issuer
version: "1.0.0"
locales: [en]
actor_kinds:
  issuer: { category: machine }
target_types:
  credential: { description: "An issued credential." }
actions:
  issuer.credential.issued:
    summary: A credential was issued.
    operation: create
    categories: [credential_lifecycle]
    profiles: [evidence]
    target_types: [credential]
    data_schema: https://schemas.example/issuer/credential.json
    message: { en: "credential {targets_0_id} issued" }
  issuer.credential.renewed:
    summary: A credential's validity was extended.
    operation: modify
    categories: [credential_lifecycle]
    profiles: [evidence]
    target_types: [credential]
    data_schema: https://schemas.example/issuer/credential.json
    extends: /renews
    message: { en: "credential {targets_0_id} renewed" }
`

type extending struct {
	writer  *writer.Writer
	store   *storetest.Memory
	records writer.Locator
	setNow  func(time.Time)
	failed  []string
}

// buildExtending is a writer with an after_expiry evidence profile, the index
// as the way to find earlier records, and its own account turned on so that
// what it extends is in the trail.
func buildExtending(t *testing.T) *extending {
	t.Helper()
	memory := index.NewMemory()
	return buildExtendingOn(t, memory, memory)
}

// buildExtendingOn is buildExtending over the given index.
func buildExtendingOn(t *testing.T, indexer index.Indexer, records writer.Locator) *extending {
	t.Helper()
	issuer, err := catalogue.Load([]byte(issuerDoc), [][]byte{[]byte(issuerSchema)})
	if err != nil {
		t.Fatal(err)
	}
	common, err := catalogue.Common()
	if err != nil {
		t.Fatal(err)
	}
	registry := &writer.Registry{}
	registry.Register(issuer)
	registry.Register(common)

	builtin, err := preset.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := preset.Compose(preset.Composition{Name: "evidence", Presets: []string{"evidence-etsi"}}, builtin)
	if err != nil {
		t.Fatal(err)
	}
	all := profiles(t)
	all["evidence"] = evidence

	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatal(err)
	}
	provider, err := keys.NewLocal(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })

	at := day(t, "2026-09-17T10:30:00Z")
	var mu sync.Mutex
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return at }
	b := &extending{store: storetest.NewMemory(), records: records,
		setNow: func(t time.Time) { mu.Lock(); defer mu.Unlock(); at = t }}
	w, err := writer.New(&writer.Writer{
		Catalogues: registry,
		Splitter:   &writer.Splitter{Profiles: all, Keys: provider},
		Roller:     &writer.Roller{Store: b.store, Instance: "writer-1", Indexer: indexer, Now: now},
		DeadLetter: &writer.StoreDeadLetter{Store: b.store, Instance: "writer-1", Now: now},
		Records:    records,
		Meta:       common,
		Identity:   func(context.Context) string { return "workload:issuer" },
		Version:    "1.0.0",
		Now:        now,
		Hooks: writer.Hooks{
			OnRetentionNotExtended: func(profile, id string, err error) {
				b.failed = append(b.failed, profile+" "+id+": "+err.Error())
			},
			OnDeadLettered: func(_ *record.Record, reason string) { t.Errorf("dead-lettered: %s", reason) },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b.writer = w
	return b
}

// credential is a record of one of the issuer's actions about cred-1.
func credential(t *testing.T, action, tenant, validUntil string, renews ...string) *record.Record {
	t.Helper()
	data := map[string]any{"valid_until": validUntil}
	if len(renews) > 0 {
		ids := make([]any, len(renews))
		for i, id := range renews {
			ids[i] = id
		}
		data["renews"] = ids
	}
	s, err := structpb.NewStruct(data)
	if err != nil {
		t.Fatal(err)
	}
	operation := auditv1.Operation_OPERATION_CREATE
	if len(renews) > 0 {
		operation = auditv1.Operation_OPERATION_MODIFY
	}
	return &record.Record{
		Id:               record.NewID(),
		OccurredAt:       timestamppb.New(day(t, "2026-09-17T10:00:00Z")),
		SchemaVersion:    record.SchemaVersion,
		CatalogueVersion: "1.0.0",
		Source:           "issuer",
		Observer:         &record.Observer{Version: "1", Instance: "issuer-1"},
		Action:           action,
		Operation:        operation,
		Outcome:          &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
		TenantId:         tenant,
		Actor:            &record.Actor{Kind: "issuer", Id: "issuer-service"},
		Targets:          []*record.Target{{Type: "credential", Id: "cred-1"}},
		Data:             s,
	}
}

func (b *extending) feed(t *testing.T, records ...*record.Record) {
	t.Helper()
	if _, err := b.writer.Write(context.Background(),
		&sink.Request{Records: records, Delivery: sink.Block}); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// lockOf is the retention of the object the index says holds a record.
func (b *extending) lockOf(t *testing.T, id string) time.Time {
	t.Helper()
	row, _, err := b.records.Get(context.Background(), "evidence", id)
	if err != nil {
		t.Fatalf("record %s is not indexed: %v", id, err)
	}
	o, ok := b.store.Object(row.ObjectKey)
	if !ok {
		t.Fatalf("object %s is not in the store", row.ObjectKey)
	}
	return o.RetainUntil
}

// extensions are the writer's records of what it extended, by outcome.
func (b *extending) extensions(t *testing.T) (ok, failed []*record.Record) {
	t.Helper()
	if err := b.writer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, r := range decode(t, b.store) {
		if r.GetAction() != "audit.retention.extended" || r.GetProfile() != "evidence" {
			continue
		}
		if r.GetOutcome().GetResult() == auditv1.Outcome_RESULT_FAILURE {
			failed = append(failed, r)
		} else {
			ok = append(ok, r)
		}
	}
	return ok, failed
}

// A renewal lengthens the lock on the issuance's object to the new expiry plus
// the profile's seven years, and says so in the trail.
func TestARenewalExtendsTheLockOnTheIssuance(t *testing.T) {
	renewalExtends(t, buildExtending(t))
}

func renewalExtends(t *testing.T, b *extending) {
	t.Helper()
	issuance := credential(t, "issuer.credential.issued", "acme", "2031-09-17T00:00:00Z")
	b.feed(t, issuance)
	if got, want := b.lockOf(t, issuance.GetId()), day(t, "2038-09-17T00:00:00Z"); !got.Equal(want) {
		t.Fatalf("the issuance is locked until %s, want %s", got, want)
	}

	later := day(t, "2027-09-17T10:30:00Z")
	b.setNow(later)
	renewal := credential(t, "issuer.credential.renewed", "acme", "2034-09-17T00:00:00Z", issuance.GetId())
	renewal.OccurredAt = timestamppb.New(later)
	b.feed(t, renewal)

	if got, want := b.lockOf(t, issuance.GetId()), day(t, "2041-09-17T00:00:00Z"); !got.Equal(want) {
		t.Fatalf("after the renewal the issuance is locked until %s, want %s", got, want)
	}
	ok, failed := b.extensions(t)
	if len(failed) != 0 || len(b.failed) != 0 {
		t.Fatalf("failures: %v %v", failed, b.failed)
	}
	if len(ok) != 1 {
		t.Fatalf("%d extensions recorded, want 1", len(ok))
	}
	targets := ok[0].GetTargets()
	if targets[0].GetId() != issuance.GetId() || targets[1].GetId() != renewal.GetId() {
		t.Fatalf("the record names %v", targets)
	}
	data := ok[0].GetData().AsMap()
	if data["retain_until"] != "2041-09-17T00:00:00Z" || data["previous_retain_until"] != "2038-09-17T00:00:00Z" {
		t.Fatalf("the record says %v", data)
	}
	if ok[0].GetTenantId() != "acme" {
		t.Fatalf("recorded under tenant %q, want the addendum's", ok[0].GetTenantId())
	}
}

// A lock is lengthened, never shortened: a later renewal that expires sooner
// than the issuance leaves it as it was, and records nothing, because nothing
// changed.
func TestAShorterRenewalChangesNothing(t *testing.T) {
	b := buildExtending(t)
	issuance := credential(t, "issuer.credential.issued", "acme", "2031-09-17T00:00:00Z")
	b.feed(t, issuance)
	before := b.lockOf(t, issuance.GetId())

	b.feed(t, credential(t, "issuer.credential.renewed", "acme", "2030-01-01T00:00:00Z", issuance.GetId()))
	if got := b.lockOf(t, issuance.GetId()); !got.Equal(before) {
		t.Fatalf("the lock moved from %s to %s", before, got)
	}
	ok, failed := b.extensions(t)
	if len(ok)+len(failed) != 0 {
		t.Fatalf("recorded %d extensions and %d failures for a change that did not happen", len(ok), len(failed))
	}
}

// The store refuses a shorter date on its own, whoever asks: a double that
// accepted it would let a test pass that a compliance-mode bucket fails.
func TestTheMemoryStoreRefusesToShortenALock(t *testing.T) {
	b := buildExtending(t)
	issuance := credential(t, "issuer.credential.issued", "acme", "2031-09-17T00:00:00Z")
	b.feed(t, issuance)
	row, _, err := b.records.Get(context.Background(), "evidence", issuance.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.store.ExtendRetention(context.Background(), row.ObjectKey,
		day(t, "2030-01-01T00:00:00Z")); err == nil {
		t.Fatal("the memory store shortened a lock")
	}
}

// An addendum speaks for its own tenant's evidence only, and an earlier record
// nobody can find is a failure in the trail and on the hook — never a failed
// batch, because the addendum itself is written either way.
func TestAnExtensionThatCannotBeMadeIsRecordedNotFatal(t *testing.T) {
	b := buildExtending(t)
	issuance := credential(t, "issuer.credential.issued", "acme", "2031-09-17T00:00:00Z")
	b.feed(t, issuance)
	before := b.lockOf(t, issuance.GetId())

	b.feed(t,
		credential(t, "issuer.credential.renewed", "globex", "2034-09-17T00:00:00Z", issuance.GetId()),
		credential(t, "issuer.credential.renewed", "acme", "2034-09-17T00:00:00Z", "no-such-record"),
	)
	if got := b.lockOf(t, issuance.GetId()); !got.Equal(before) {
		t.Fatalf("another tenant's addendum moved the lock to %s", got)
	}
	ok, failed := b.extensions(t)
	if len(ok) != 0 || len(failed) != 2 || len(b.failed) != 2 {
		t.Fatalf("%d extended, %d failures recorded, %d reported", len(ok), len(failed), len(b.failed))
	}
	reasons := failed[0].GetOutcome().GetReason() + " | " + failed[1].GetOutcome().GetReason()
	if !strings.Contains(reasons, "another tenant") || !strings.Contains(reasons, "no-such-record") {
		t.Fatalf("reasons: %s", reasons)
	}
}

// On a store with no lock -- one without the Object Lock API, or a
// deployment whose profiles demand none -- there is no retention to lengthen.
// The store says so as store.ErrNotLockable, and the writer treats it as it
// treats any extension it cannot make: the addendum is written, the failure
// is in the trail with the reason, the hook fires, and nothing is fatal. A
// deployment that composes an after_expiry profile on such a store learns it
// from the trail rather than from a writer that stopped.
func TestAnExtensionOnAnUnlockedStoreIsRecordedAsNotLockable(t *testing.T) {
	b := buildExtending(t)
	b.store.Unlocked = true
	issuance := credential(t, "issuer.credential.issued", "acme", "2031-09-17T00:00:00Z")
	b.feed(t, issuance)
	if got := b.lockOf(t, issuance.GetId()); !got.IsZero() {
		t.Fatalf("an unlocked store kept a retention %s", got)
	}

	b.setNow(day(t, "2027-09-17T10:30:00Z"))
	renewal := credential(t, "issuer.credential.renewed", "acme", "2034-09-17T00:00:00Z", issuance.GetId())
	renewal.OccurredAt = timestamppb.New(day(t, "2027-09-17T10:30:00Z"))
	b.feed(t, renewal)

	ok, failed := b.extensions(t)
	if len(ok) != 0 || len(failed) != 1 || len(b.failed) != 1 {
		t.Fatalf("%d extended, %d failures recorded, %d reported", len(ok), len(failed), len(b.failed))
	}
	if reason := failed[0].GetOutcome().GetReason(); !strings.Contains(reason, store.ErrNotLockable.Error()) {
		t.Fatalf("the trail should say the store holds no lock, said: %s", reason)
	}
	if failed[0].GetOutcome().GetCode() != "not_extended" {
		t.Fatalf("code = %q", failed[0].GetOutcome().GetCode())
	}
}
