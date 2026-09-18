// Package authtest is an identity provider for tests: a discovery document and
// a key set served over a real listener, so the code under test runs its
// actual discovery and key fetch rather than being handed a key.
package authtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v4/jwa"
	"github.com/lestrrat-go/jwx/v4/jwk"
	"github.com/lestrrat-go/jwx/v4/jwt"
)

// Issuer is one identity provider. A cluster's service-account issuer is one of
// these too, as far as the code under test can tell.
type Issuer struct {
	URL    string
	signer jwk.Key
}

// NewIssuer starts an issuer that stops when the test ends.
func NewIssuer(t *testing.T) *Issuer {
	t.Helper()
	raw, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jwk.Import[jwk.Key](raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.Set(jwk.KeyIDKey, "k1"); err != nil {
		t.Fatal(err)
	}
	if err := signer.Set(jwk.AlgorithmKey, jwa.ES256()); err != nil {
		t.Fatal(err)
	}
	public, err := jwk.PublicKeyOf(signer)
	if err != nil {
		t.Fatal(err)
	}
	is := &Issuer{signer: signer}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": is.URL, "jwks_uri": is.URL + "/keys"})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		set := jwk.NewSet()
		_ = set.AddKey(public)
		_ = json.NewEncoder(w).Encode(set)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	is.URL = server.URL
	return is
}

// Token mints a token for subject with audience "audit", signed by this issuer
// and naming from as its issuer — which is this one unless a test is forging.
// mutate says what else is true of it.
func (is *Issuer) Token(t *testing.T, from, subject string, mutate func(*jwt.Builder)) string {
	t.Helper()
	b := jwt.NewBuilder().Issuer(from).Subject(subject).Audience([]string{"audit"}).
		IssuedAt(time.Now()).Expiration(time.Now().Add(time.Hour))
	if mutate != nil {
		mutate(b)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(built, jwt.WithKey(jwa.ES256(), is.signer))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

// ServiceAccount is the subject a cluster gives a service account's token.
func ServiceAccount(namespace, name string) string {
	return "system:serviceaccount:" + namespace + ":" + name
}
