package cli_test

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/internal/cli"
	"github.com/truvity/audit/internal/hold"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store"
	"github.com/truvity/audit/store/storetest"
)

func destroyer(t *testing.T, into sink.Sink) (cli.KeyDestroy, *storetest.Memory, *keys.Local) {
	t.Helper()
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatal(err)
	}
	provider, err := keys.NewLocal(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })

	s := storetest.NewMemory()
	return cli.KeyDestroy{
		Provider: provider, Store: s, Sink: into, Catalogue: common(t),
		Tenant: "acme", Purpose: "security", By: "olga", Reason: "erasure requested",
		Out: &strings.Builder{},
	}, s, provider
}

// held puts an object and a hold over it.
func held(t *testing.T, s *storetest.Memory, profile, tenant string) {
	t.Helper()
	ctx := context.Background()
	key := "profile=" + profile + "/tenant=" + tenant + "/year=2026/month=09/day=17/a.ndjson.zst"
	if err := s.Put(ctx, store.Object{
		Key: key, Body: []byte("{}"), RetainUntil: at(t, "2027-09-17T00:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := (hold.Store{Store: s}).Place(ctx, hold.Record{
		ID: "h-1", Profile: profile, Tenant: tenant, Reason: "matter 2026-11", PlacedBy: "olga",
	}); err != nil {
		t.Fatal(err)
	}
}

// Erasure is what the key destroy is for, and the pseudonyms must stop being
// recomputable. That is the whole of the guarantee.
func TestKeyDestroyMakesThePseudonymUnrecomputable(t *testing.T) {
	into := &collector{}
	run, _, provider := destroyer(t, into)
	ctx := context.Background()

	before, err := provider.Pseudonym(ctx, "acme", "security", "person-1")
	if err != nil {
		t.Fatal(err)
	}
	if before == "" {
		t.Fatal("no pseudonym")
	}
	if err := run.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Pseudonym(ctx, "acme", "security", "person-1"); err == nil {
		t.Fatal("the key was destroyed and a pseudonym was still computed")
	}
}

// Crypto-shredding a tenant whose copies are under legal hold destroys evidence
// that may not be destroyed. The operator is not the check.
func TestKeyDestroyRefusesUnderALegalHold(t *testing.T) {
	into := &collector{}
	run, s, provider := destroyer(t, into)
	held(t, s, "security", "acme")
	ctx := context.Background()

	err := run.Run(ctx)
	if err == nil {
		t.Fatal("a key under an active legal hold must not be destroyed")
	}
	for _, want := range []string{"h-1", "matter 2026-11", "may not be destroyed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say %q: %v", want, err)
		}
	}
	if len(into.records) != 0 {
		t.Fatal("a refused erasure was recorded as though it happened")
	}
	// And the key is still there.
	if _, err := provider.Pseudonym(ctx, "acme", "security", "person-1"); err != nil {
		t.Fatalf("the key was destroyed despite the refusal: %v", err)
	}
}

// Released, the hold no longer stands in the way.
func TestKeyDestroyProceedsOnceTheHoldIsReleased(t *testing.T) {
	into := &collector{}
	run, s, _ := destroyer(t, into)
	held(t, s, "security", "acme")
	ctx := context.Background()

	if _, err := (hold.Store{Store: s}).Release(ctx, "h-1", "break-glass"); err != nil {
		t.Fatal(err)
	}
	if err := run.Run(ctx); err != nil {
		t.Fatalf("a released hold must not block an erasure: %v", err)
	}
}

// A hold over another tenant is not this tenant's business.
func TestKeyDestroyIgnoresAHoldOverAnotherTenant(t *testing.T) {
	into := &collector{}
	run, s, _ := destroyer(t, into)
	held(t, s, "security", "globex")
	if err := run.Run(context.Background()); err != nil {
		t.Fatalf("a hold over another tenant blocked this one: %v", err)
	}
}

// The erasure is recorded, naming the key, the tenant and the person who did
// it. The catalogue declares this action block delivery for that reason.
func TestKeyDestroyRecordsWhoErasedWhat(t *testing.T) {
	into := &collector{}
	run, _, _ := destroyer(t, into)
	if err := run.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(into.records) != 1 {
		t.Fatalf("%d records", len(into.records))
	}
	r := into.records[0]
	if r.GetAction() != "audit.key.destroyed" {
		t.Fatalf("action %q", r.GetAction())
	}
	if r.GetActor().GetId() != "olga" || r.GetActor().GetKind() != "operator" {
		t.Fatalf("actor %+v, want the operator who ran it", r.GetActor())
	}
	if r.GetOperation() != auditv1.Operation_OPERATION_REMOVE {
		t.Fatalf("operation %v", r.GetOperation())
	}
	targets := map[string]string{}
	for _, tg := range r.GetTargets() {
		targets[tg.GetType()] = tg.GetId()
	}
	if targets["key"] != "acme/security" || targets["tenant"] != "acme" {
		t.Fatalf("targets %v, want the key and the tenant the catalogue declares", targets)
	}
}

// An erasure the trail does not record is one nobody can prove was lawful, so
// the failure has to name what is now true: the key is gone.
func TestKeyDestroySaysSoWhenTheRecordFails(t *testing.T) {
	refusing := sink.Func(func(context.Context, *sink.Request) (*sink.Result, error) {
		return nil, errors.New("the writer is unreachable")
	})
	run, _, provider := destroyer(t, refusing)
	ctx := context.Background()

	err := run.Run(ctx)
	if err == nil {
		t.Fatal("a record that could not be written must be an error")
	}
	if !strings.Contains(err.Error(), "IS DESTROYED") {
		t.Errorf("the error must say the key is already gone: %v", err)
	}
	if _, err := provider.Pseudonym(ctx, "acme", "security", "person-1"); err == nil {
		t.Fatal("the error implies the key is gone and it is not")
	}
}

// Irreversible, so it refuses to be done anonymously or without a writer.
func TestKeyDestroyRefusesWhatItCannotAccountFor(t *testing.T) {
	for _, c := range []struct {
		name string
		with func(*cli.KeyDestroy)
		want string
	}{
		{"nobody named", func(k *cli.KeyDestroy) { k.By = " " }, "who is destroying"},
		{"no writer", func(k *cli.KeyDestroy) { k.Sink = nil }, "block delivery"},
		{"no archive", func(k *cli.KeyDestroy) { k.Store = nil }, "legal holds"},
		{"no tenant", func(k *cli.KeyDestroy) { k.Tenant = "" }, "tenant and the purpose"},
	} {
		t.Run(c.name, func(t *testing.T) {
			run, _, _ := destroyer(t, &collector{})
			c.with(&run)
			err := run.Run(context.Background())
			if err == nil {
				t.Fatal("want a refusal")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the refusal should say %q: %v", c.want, err)
			}
		})
	}
}
