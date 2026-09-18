package query_test

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/catalogue"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/internal/identity"
	"github.com/truvity/audit/internal/query"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store/storetest"
)

// resolving builds a service that can resolve, with one person's identity kept
// under acme's security key, the pseudonym for it, and the keys.
func resolving(t *testing.T, g auth.Grant, to sink.Sink) (*query.Service, string, *keys.Local) {
	t.Helper()
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatal(err)
	}
	provider, err := keys.NewLocal(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pseudonym, err := provider.Pseudonym(ctx, "acme", "security", "alice")
	if err != nil {
		t.Fatal(err)
	}
	m := &identity.Map{Store: storetest.NewMemory(), Keys: provider}
	if err := m.Remember(ctx, "acme", "security", pseudonym, "alice"); err != nil {
		t.Fatal(err)
	}
	common, err := catalogue.Common()
	if err != nil {
		t.Fatal(err)
	}
	s, err := query.New(&query.Service{
		Searcher:   corpus(t),
		Authorizer: auth.Declarative{Rules: []auth.Rule{{Name: "dpo-resolve", Grant: g}}},
		Sink:       to, Catalogue: common, Instance: "query-1", Identities: m,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, pseudonym, provider
}

func resolveGrant() auth.Grant {
	return auth.Grant{Tenants: []string{"acme"}, Profiles: []string{"security"},
		Operations: []auth.Operation{auth.Resolve}}
}

func ask(pseudonym string) *auditv1.ResolveRequest {
	return &auditv1.ResolveRequest{Profile: "security", TenantId: "acme", Pseudonym: pseudonym}
}

// A grant with resolve gets the identity back, and the trail says who resolved
// which pseudonym under which rule — and never the identity.
func TestResolveReturnsTheIdentityAndRecordsItFirst(t *testing.T) {
	into := &reads{}
	s, pseudonym, _ := resolving(t, resolveGrant(), into)
	id, err := s.Resolve(context.Background(), caller(), ask(pseudonym))
	if err != nil || id != "alice" {
		t.Fatalf("resolve: %q %v", id, err)
	}
	got := into.of("audit.pseudonym.resolved")
	if len(got) != 1 {
		t.Fatalf("%d records of the resolution", len(got))
	}
	for _, target := range got[0].GetTargets() {
		if target.GetId() == "alice" {
			t.Fatal("the record of a resolution names the identity it resolved to")
		}
	}
	if got[0].GetTargets()[0].GetId() != pseudonym || got[0].GetOutcome().GetReason() != "dpo-resolve" {
		t.Fatalf("record %+v", got[0])
	}
}

// Every fence holds.
func TestResolveIsFenced(t *testing.T) {
	ctx := context.Background()

	t.Run("a read grant does not resolve", func(t *testing.T) {
		g := resolveGrant()
		g.Operations = []auth.Operation{auth.Search, auth.Get}
		s, pseudonym, _ := resolving(t, g, &reads{})
		if _, err := s.Resolve(ctx, caller(), ask(pseudonym)); !errors.Is(err, auth.ErrDenied) {
			t.Fatalf("a read grant resolved: %v", err)
		}
	})
	t.Run("another tenant's pseudonym reads as unknown", func(t *testing.T) {
		g := resolveGrant()
		g.Tenants = []string{"globex"}
		s, pseudonym, _ := resolving(t, g, &reads{})
		if _, err := s.Resolve(ctx, caller(), ask(pseudonym)); !errors.Is(err, query.ErrNotFound) {
			t.Fatalf("another tenant's pseudonym: %v", err)
		}
	})
	t.Run("a resolution the trail cannot take is not made", func(t *testing.T) {
		refusing := sink.Func(func(context.Context, *sink.Request) (*sink.Result, error) {
			return nil, errors.New("the writer is unreachable")
		})
		s, pseudonym, _ := resolving(t, resolveGrant(), refusing)
		if id, err := s.Resolve(ctx, caller(), ask(pseudonym)); err == nil || id != "" {
			t.Fatalf("resolved off the record: %q %v", id, err)
		}
	})
	t.Run("after erasure the way back is gone, and it says so", func(t *testing.T) {
		s, pseudonym, provider := resolving(t, resolveGrant(), &reads{})
		if err := provider.Destroy(ctx, "acme", "security"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Resolve(ctx, caller(), ask(pseudonym)); !errors.Is(err, query.ErrErased) {
			t.Fatalf("after the key was destroyed: %v", err)
		}
	})
	t.Run("a pseudonym nobody minted is unknown", func(t *testing.T) {
		s, _, _ := resolving(t, resolveGrant(), &reads{})
		if _, err := s.Resolve(ctx, caller(), ask("ps_nobody")); !errors.Is(err, query.ErrNotFound) {
			t.Fatalf("an unknown pseudonym: %v", err)
		}
	})
}
