package keys_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/keys"
)

// baoIn makes one set-up call inside a namespace, as root.
func baoIn(t *testing.T, url, token, namespace, method, path string, body any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(method, url+"/v1/"+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Vault-Token", token)
	if namespace != "" {
		req.Header.Set("X-Vault-Namespace", namespace)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		var out bytes.Buffer
		_, _ = out.ReadFrom(res.Body)
		t.Fatalf("%s %s in %q: %s %s", method, path, namespace, res.Status, out.String())
	}
}

// cluster is an environment namespace set up the way the estate's are: a
// transit engine, and a JWT auth mount that takes a cluster's service-account
// tokens, with one role for the audit writer.
type cluster struct {
	url, namespace, mount string
	key                   ed25519.PrivateKey
}

const writerSubject = "system:serviceaccount:audit:audit"

func newCluster(t *testing.T, url, root string, tokenTTL string) *cluster {
	t.Helper()
	c := &cluster{url: url, namespace: "env-" + strings.ToLower(rand.Text()[:8]), mount: "jwt-devel"}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c.key = private
	der, _ := x509.MarshalPKIXPublicKey(public)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	baoIn(t, url, root, "", http.MethodPost, "sys/namespaces/"+c.namespace, map[string]any{})
	baoIn(t, url, root, c.namespace, http.MethodPost, "sys/mounts/transit", map[string]string{"type": "transit"})
	baoIn(t, url, root, c.namespace, http.MethodPost, "sys/auth/"+c.mount, map[string]string{"type": "jwt"})
	baoIn(t, url, root, c.namespace, http.MethodPost, "auth/"+c.mount+"/config", map[string]any{
		"jwt_validation_pubkeys": []string{pubPEM},
		"jwt_supported_algs":     []string{"EdDSA"},
	})
	baoIn(t, url, root, c.namespace, http.MethodPost, "sys/policies/acl/audit-writer", map[string]string{"policy": `
path "transit/hmac/audit.security.*"    { capabilities = ["update"] }
path "transit/encrypt/audit.security.*" { capabilities = ["create", "update"] }
path "transit/sign/audit-digest"        { capabilities = ["update"] }
path "transit/keys/audit-digest"        { capabilities = ["read"] }
`})
	baoIn(t, url, root, c.namespace, http.MethodPost, "auth/"+c.mount+"/role/audit-writer", map[string]any{
		"role_type":       "jwt",
		"bound_audiences": []string{"openbao"},
		"bound_subject":   writerSubject,
		"user_claim":      "sub",
		"token_policies":  []string{"audit-writer"},
		"token_ttl":       tokenTTL,
		"token_max_ttl":   tokenTTL,
	})
	return c
}

// projected writes a service-account token as the kubelet would project it,
// signed by the cluster's key.
func (c *cluster) projected(t *testing.T, audience string) string {
	t.Helper()
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT"})
	now := time.Now()
	claims, _ := json.Marshal(map[string]any{
		"sub": writerSubject, "aud": []string{audience},
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	})
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	token := signing + "." + enc.EncodeToString(ed25519.Sign(c.key, []byte(signing)))
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (c *cluster) login(file string) *keys.JWTLogin {
	return &keys.JWTLogin{Mount: c.mount, Role: "audit-writer", TokenFile: file}
}

// The writer signs in the way the estate's workloads do — its projected
// service-account token on the cluster's JWT mount, inside the environment's
// namespace — and pseudonymises there. No token is stored anywhere.
func TestTransitSignsInWithAProjectedTokenInANamespace(t *testing.T) {
	url, root := openbao(t)
	ctx := context.Background()
	c := newCluster(t, url, root, "1h")

	p, err := keys.NewTransit(ctx, &keys.Transit{
		Address: url, Namespace: c.namespace, Login: c.login(c.projected(t, "openbao")),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Pseudonym(ctx, "acme", "security", "alice")
	if err != nil {
		t.Fatalf("pseudonymising after a JWT login: %v", err)
	}
	// The same key as root sees it in that namespace, and not the root
	// namespace's: the namespace is where the key lives.
	admin, err := keys.NewTransit(ctx, &keys.Transit{Address: url, Namespace: c.namespace, Token: root})
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := admin.Pseudonym(ctx, "acme", "security", "alice"); want != got {
		t.Fatalf("the login's pseudonym %s is not the namespace's %s", got, want)
	}
	if _, err := p.Pseudonym(ctx, "acme", "billing", "alice"); err == nil {
		t.Fatal("the writer's role pseudonymised for a purpose its policy does not name")
	}

	// A session revoked before its lease ends is answered by signing in again.
	baoIn(t, url, root, c.namespace, http.MethodPost, "sys/leases/revoke-prefix/auth/"+c.mount, map[string]any{})
	if again, err := p.Pseudonym(ctx, "acme", "security", "alice"); err != nil || again != got {
		t.Fatalf("after the session was revoked: %q %v", again, err)
	}
}

// A session whose lease runs out is replaced before a call is refused.
func TestTransitSignsInAgainAsTheLeaseRunsOut(t *testing.T) {
	url, root := openbao(t)
	ctx := context.Background()
	c := newCluster(t, url, root, "2s")
	p, err := keys.NewTransit(ctx, &keys.Transit{
		Address: url, Namespace: c.namespace, Login: c.login(c.projected(t, "openbao")),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.Pseudonym(ctx, "acme", "security", "alice")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	if again, err := p.Pseudonym(ctx, "acme", "security", "alice"); err != nil || again != first {
		t.Fatalf("after the lease ran out: %q %v", again, err)
	}
}

// A token the role would refuse stops the provider when it is built, not on
// the first record.
func TestTransitRefusesALoginAtStart(t *testing.T) {
	url, root := openbao(t)
	c := newCluster(t, url, root, "1h")
	_, err := keys.NewTransit(context.Background(), &keys.Transit{
		Address: url, Namespace: c.namespace, Login: c.login(c.projected(t, "somebody-else")),
	})
	if err == nil {
		t.Fatal("a token for another audience signed in")
	}
}

// The digest signer signs in the same way.
func TestTransitSignerSignsInWithAProjectedToken(t *testing.T) {
	url, root := openbao(t)
	ctx := context.Background()
	c := newCluster(t, url, root, "1h")
	baoIn(t, url, root, c.namespace, http.MethodPost, "transit/keys/audit-digest", map[string]string{"type": "ed25519"})

	s, err := keys.NewTransitSigner(ctx, &keys.TransitSigner{
		Address: url, Namespace: c.namespace, Key: "audit-digest", Login: c.login(c.projected(t, "openbao")),
	})
	if err != nil {
		t.Fatal(err)
	}
	signature, err := s.Sign(ctx, []byte("window"))
	if err != nil {
		t.Fatal(err)
	}
	public, _ := s.PublicKey(ctx)
	if err := keys.Verify(public, []byte("window"), signature); err != nil {
		t.Fatal(err)
	}
}

// A server whose certificate comes from a private chain is reached with that
// chain's bundle, and refused without it. The engine's API is not what is
// under test here, only the handshake, so a TLS stand-in answers.
func TestTransitTrustsThePrivateChainItIsGiven(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Namespace") != "devel" {
			http.Error(w, `{"errors":["wrong namespace"]}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer server.Close()
	bundle := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(bundle, pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: server.Certificate().Raw,
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := keys.NewTransit(ctx, &keys.Transit{
		Address: server.URL, Namespace: "devel", Token: "t", CAFile: bundle,
	}); err != nil {
		t.Fatalf("with the bundle: %v", err)
	}
	if _, err := keys.NewTransit(ctx, &keys.Transit{
		Address: server.URL, Namespace: "devel", Token: "t",
	}); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("without the bundle the handshake should fail on the certificate: %v", err)
	}
}
