package keys_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/truvity/audit/keys"
)

// transitProvider is a provider on the dev server under a prefix of its own,
// since the server outlives a test run and a key destroyed in the last one
// stays destroyed.
func transitProvider(t *testing.T, url, token, prefix string) *keys.Transit {
	t.Helper()
	bao(t, url, token, http.MethodPost, "sys/mounts/transit", map[string]string{"type": "transit"})
	p, err := keys.NewTransit(context.Background(), &keys.Transit{Address: url, Token: token, Prefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func runPrefix() string { return "audit-" + strings.ToLower(rand.Text()[:8]) }

// policyToken writes a policy and returns a token that has only it, which is
// how a deployment scopes a role to its purposes.
func policyToken(t *testing.T, url, root, name, policy string) string {
	t.Helper()
	bao(t, url, root, http.MethodPost, "sys/policies/acl/"+name, map[string]string{"policy": policy})
	raw, _ := json.Marshal(map[string]any{"policies": []string{name}, "ttl": "10m"})
	req, err := http.NewRequest(http.MethodPost, url+"/v1/auth/token/create", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Vault-Token", root)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var out struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || out.Auth.ClientToken == "" {
		t.Fatalf("creating a token for %s: %v", name, err)
	}
	return out.Auth.ClientToken
}

// Two replicas with nothing in common but the engine give the same person the
// same pseudonym — the property Local only has on a shared directory — and a
// different one for another purpose or another tenant.
func TestTransitReplicasAgreeAndPurposesDoNot(t *testing.T) {
	url, token := openbao(t)
	prefix := runPrefix()
	ctx := context.Background()
	one, two := transitProvider(t, url, token, prefix), transitProvider(t, url, token, prefix)

	a, err := one.Pseudonym(ctx, "acme", "security", "alice")
	if err != nil {
		t.Fatal(err)
	}
	b, err := two.Pseudonym(ctx, "acme", "security", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if a != b || !keys.IsPseudonym(a) {
		t.Fatalf("two replicas disagree: %s and %s", a, b)
	}
	for name, other := range map[string]func() (string, error){
		"another purpose": func() (string, error) { return one.Pseudonym(ctx, "acme", "billing", "alice") },
		"another tenant":  func() (string, error) { return one.Pseudonym(ctx, "globex", "security", "alice") },
		"another person":  func() (string, error) { return one.Pseudonym(ctx, "acme", "security", "bob") },
	} {
		got, err := other()
		if err != nil {
			t.Fatal(err)
		}
		if got == a {
			t.Errorf("%s gave the same pseudonym", name)
		}
	}
	if _, err := one.Pseudonym(ctx, "acme", "sec.urity", "alice"); err == nil {
		t.Fatal("a purpose with a dot, which could name another tenant's key, was accepted")
	}
}

// After destroy nothing computes the pseudonym or opens what was sealed —
// not this provider, and not a fresh one that has never seen the key, which
// is the restart that must not mint a replacement.
func TestTransitDestroyLeavesATombstone(t *testing.T) {
	url, token := openbao(t)
	prefix := runPrefix()
	ctx := context.Background()
	p := transitProvider(t, url, token, prefix)

	if _, err := p.Pseudonym(ctx, "acme", "security", "alice"); err != nil {
		t.Fatal(err)
	}
	sealed, err := p.Seal(ctx, "acme", "security", []byte("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := p.Open(ctx, "acme", "security", sealed); err != nil || string(plain) != "alice" {
		t.Fatalf("open before destroy: %q %v", plain, err)
	}

	if err := p.Destroy(ctx, "acme", "security"); err != nil {
		t.Fatal(err)
	}
	if err := p.Destroy(ctx, "acme", "security"); err != nil {
		t.Fatalf("destroying twice: %v", err)
	}
	restarted := transitProvider(t, url, token, prefix)
	if _, err := restarted.Pseudonym(ctx, "acme", "security", "alice"); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("after destroy a fresh provider computed a pseudonym: %v", err)
	}
	if _, err := restarted.Open(ctx, "acme", "security", sealed); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("after destroy a sealed identifier opened: %v", err)
	}
	if _, err := restarted.Seal(ctx, "acme", "security", []byte("bob")); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("after destroy something was sealed: %v", err)
	}

	// A tenant erased before it was ever seen stays erased when it appears.
	if err := p.Destroy(ctx, "initech", "security"); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Pseudonym(ctx, "initech", "security", "alice"); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("a tenant destroyed before first use was keyed afterwards: %v", err)
	}
}

// Scoping is the engine's: a metering role granted its own purpose cannot
// pseudonymise for security, and a writer's role cannot destroy. The policies
// are the ones docs/operations/openbao-keys.md tells a deployment to write.
func TestTransitScopeIsTheEnginesPolicy(t *testing.T) {
	url, root := openbao(t)
	prefix := runPrefix()
	ctx := context.Background()
	transitProvider(t, url, root, prefix)

	metering := policyToken(t, url, root, prefix+"-metering", `
path "transit/hmac/`+prefix+`.billing.*"    { capabilities = ["update"] }
path "transit/encrypt/`+prefix+`.billing.*" { capabilities = ["create", "update"] }
`)
	p, err := keys.NewTransit(ctx, &keys.Transit{Address: url, Token: metering, Prefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Pseudonym(ctx, "acme", "billing", "alice"); err != nil {
		t.Fatalf("the metering role could not use its own purpose: %v", err)
	}
	if _, err := p.Pseudonym(ctx, "acme", "security", "alice"); err == nil {
		t.Fatal("the metering role pseudonymised for security")
	}
	if err := p.Destroy(ctx, "acme", "billing"); err == nil {
		t.Fatal("a role that may only pseudonymise destroyed a key")
	}
	admin := transitProvider(t, url, root, prefix)
	if _, err := admin.Pseudonym(ctx, "acme", "billing", "alice"); err != nil {
		t.Fatalf("the key was harmed by the refused destroy: %v", err)
	}
}

// The erasure operator's policy, as the operations page gives it, is enough to
// destroy — including a tenant never seen — and still grants no delete.
func TestTransitEraserPolicyIsEnough(t *testing.T) {
	url, root := openbao(t)
	prefix := runPrefix()
	ctx := context.Background()
	writer := transitProvider(t, url, root, prefix)
	if _, err := writer.Pseudonym(ctx, "acme", "security", "alice"); err != nil {
		t.Fatal(err)
	}
	eraser := policyToken(t, url, root, prefix+"-eraser", `
path "transit/keys/`+prefix+`.*"    { capabilities = ["read", "update"] }
path "transit/encrypt/`+prefix+`.*" { capabilities = ["create", "update"] }
`)
	p, err := keys.NewTransit(ctx, &keys.Transit{Address: url, Token: eraser, Prefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{"acme", "never-seen"} {
		if err := p.Destroy(ctx, tenant, "security"); err != nil {
			t.Fatalf("the eraser could not destroy %s: %v", tenant, err)
		}
		if _, err := writer.Pseudonym(ctx, tenant, "security", "alice"); !errors.Is(err, keys.ErrDestroyed) {
			t.Fatalf("%s: %v", tenant, err)
		}
	}
	req, _ := http.NewRequest(http.MethodDelete, url+"/v1/transit/keys/"+prefix+".security.acme", nil)
	req.Header.Set("X-Vault-Token", eraser)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("the eraser's policy let it delete a tombstone: %s", res.Status)
	}
}
