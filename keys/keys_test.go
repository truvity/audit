package keys_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/truvity/audit/keys"
)

func local(t *testing.T, dir string) *keys.Local {
	t.Helper()
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatal(err)
	}
	p, err := keys.NewLocal(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// The same identifier under the same key is the same pseudonym, or the trail
// could not answer what one person did twice.
func TestPseudonymIsStable(t *testing.T) {
	p := local(t, t.TempDir())
	ctx := context.Background()

	first, err := p.Pseudonym(ctx, "acme", "security", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !keys.IsPseudonym(first) {
		t.Fatalf("%q is not marked as a pseudonym; a reader could mistake it for an identifier", first)
	}
	for i := 0; i < 10; i++ {
		again, err := p.Pseudonym(ctx, "acme", "security", "alice")
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatalf("the pseudonym changed: %s then %s", first, again)
		}
	}
	other, err := p.Pseudonym(ctx, "acme", "security", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("two people share a pseudonym")
	}
}

// This is the property the two-prefix design rests on: the security copy and
// the billing copy of one event must not be joinable on a person.
func TestPseudonymsDoNotJoinAcrossPurposesOrTenants(t *testing.T) {
	p := local(t, t.TempDir())
	ctx := context.Background()

	security, err := p.Pseudonym(ctx, "acme", "security", "alice")
	if err != nil {
		t.Fatal(err)
	}
	billing, err := p.Pseudonym(ctx, "acme", "billing", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if security == billing {
		t.Fatal("one person has the same pseudonym in two purposes; the copies could be joined")
	}
	otherTenant, err := p.Pseudonym(ctx, "other", "security", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if otherTenant == security {
		t.Fatal("one identifier has the same pseudonym in two tenants")
	}
}

// Two writer replicas must agree, or one person would appear as two.
func TestTwoProvidersOnOneRootAgree(t *testing.T) {
	dir := t.TempDir()
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatal(err)
	}
	first, err := keys.NewLocal(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := keys.NewLocal(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	a, err := first.Pseudonym(ctx, "acme", "security", "alice")
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Pseudonym(ctx, "acme", "security", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("two replicas disagree: %s and %s", a, b)
	}
}

// Erasure is key destruction: the pseudonyms stay in the archive and nothing
// can recompute them.
func TestDestroyMakesThePseudonymUnrecomputable(t *testing.T) {
	dir := t.TempDir()
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatal(err)
	}
	p, err := keys.NewLocal(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	before, err := p.Pseudonym(ctx, "acme", "security", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Destroy(ctx, "acme", "security"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Pseudonym(ctx, "acme", "security", "alice"); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("after destruction the key must be gone, got %v", err)
	}

	// A restart must not mint a fresh key and hand the same person a second
	// identity in the same trail.
	restarted, err := keys.NewLocal(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Pseudonym(ctx, "acme", "security", "alice"); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("a restart revived a destroyed key: %v", err)
	}

	// Another purpose is untouched: erasure is per tenant and per purpose, and
	// the billing copy is kept under a legal duty.
	if _, err := restarted.Pseudonym(ctx, "acme", "billing", "alice"); err != nil {
		t.Fatalf("destroying one purpose must not touch another: %v", err)
	}
	_ = before
}

// A key that does not open under the root it is offered is an error, not a
// reason to mint a new one.
func TestAWrongRootIsRefused(t *testing.T) {
	dir := t.TempDir()
	first, second := make([]byte, 32), make([]byte, 32)
	if _, err := rand.Read(first); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(second); err != nil {
		t.Fatal(err)
	}
	a, err := keys.NewLocal(first, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Pseudonym(context.Background(), "acme", "security", "alice"); err != nil {
		t.Fatal(err)
	}

	b, err := keys.NewLocal(second, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Pseudonym(context.Background(), "acme", "security", "alice"); err == nil {
		t.Fatal("a key that does not open under this root must be refused")
	}
}

func TestNewLocalChecksTheRoot(t *testing.T) {
	if _, err := keys.NewLocal([]byte("too short"), ""); err == nil {
		t.Fatal("a root of the wrong length must be refused, not stretched")
	}
	if _, err := keys.NewLocal(nil, ""); err != nil {
		t.Fatalf("a generated root is allowed for tests: %v", err)
	}
}

// Tenant and purpose end up in paths and policies.
func TestNamesAreChecked(t *testing.T) {
	p := local(t, t.TempDir())
	ctx := context.Background()
	for _, tc := range []struct{ tenant, purpose string }{
		{"../escape", "security"},
		{"acme", "../escape"},
		{"", "security"},
		{"acme", ""},
		{strings.Repeat("a", 200), "security"},
	} {
		if _, err := p.Pseudonym(ctx, tc.tenant, keys.Purpose(tc.purpose), "alice"); err == nil {
			t.Errorf("tenant %q purpose %q was accepted", tc.tenant, tc.purpose)
		}
	}
}

func TestAnEmptyIdentifierHasNoPseudonym(t *testing.T) {
	p := local(t, "")
	got, err := p.Pseudonym(context.Background(), "acme", "security", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("an absent identifier became %q", got)
	}
}

// Two writers sharing one key directory must hand the same person the same
// pseudonym, including when both create that tenant's key at the same moment.
// Before keys were published create-once, each kept the key it minted in
// memory and the file said whichever landed last.
func TestTwoProvidersOnOneDirectoryAgreeUnderContention(t *testing.T) {
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	a, err := keys.NewLocal(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := keys.NewLocal(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	const tenants = 200
	got := make([][2]string, tenants)
	var wg sync.WaitGroup
	for n := 0; n < tenants; n++ {
		for side, p := range []*keys.Local{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				pseudonym, err := p.Pseudonym(ctx, fmt.Sprintf("tenant-%d", n), "security", "person-1")
				if err != nil {
					t.Error(err)
					return
				}
				got[n][side] = pseudonym
			}()
		}
	}
	wg.Wait()
	for n, pair := range got {
		if pair[0] != pair[1] {
			t.Fatalf("tenant-%d: the two writers disagree about the same person: %s vs %s", n, pair[0], pair[1])
		}
	}
}

// A directory has one identity, the same from every provider that opens it,
// and a different directory has a different one.
func TestADirectoryHasOneIdentity(t *testing.T) {
	root := make([]byte, 32)
	dir := t.TempDir()
	a, _ := keys.NewLocal(root, dir)
	b, _ := keys.NewLocal(root, dir)
	other, _ := keys.NewLocal(root, t.TempDir())
	idA, err := a.DirectoryID()
	if err != nil {
		t.Fatal(err)
	}
	idB, _ := b.DirectoryID()
	idOther, _ := other.DirectoryID()
	if idA == "" || idA != idB {
		t.Fatalf("one directory, two identities: %q %q", idA, idB)
	}
	if idA == idOther {
		t.Fatal("two directories share an identity")
	}
}
