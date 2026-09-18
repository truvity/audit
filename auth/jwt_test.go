package auth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v4/jwa"
	"github.com/lestrrat-go/jwx/v4/jwk"
	"github.com/lestrrat-go/jwx/v4/jwt"

	"github.com/truvity/audit/auth"
)

// issuer is an identity provider served over a real listener, so the
// authenticator runs its actual discovery and key fetch rather than being
// handed a key.
type issuer struct {
	server *httptest.Server
	signer jwk.Key
}

func newIssuer(t *testing.T) *issuer {
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
	is := &issuer{signer: signer}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": is.server.URL, "jwks_uri": is.server.URL + "/keys",
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		set := jwk.NewSet()
		_ = set.AddKey(public)
		_ = json.NewEncoder(w).Encode(set)
	})
	is.server = httptest.NewServer(mux)
	t.Cleanup(is.server.Close)
	return is
}

// token mints a token this issuer signed, as `from` claims it; mutate says
// what is wrong with it, if anything.
func (is *issuer) token(t *testing.T, from string, mutate func(*jwt.Builder)) string {
	t.Helper()
	b := jwt.NewBuilder().Issuer(from).Subject("u-1").Audience([]string{"audit"}).
		IssuedAt(time.Now()).Expiration(time.Now().Add(time.Hour)).
		Claim("groups", []string{"all:audit:auditor"})
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

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func bearer(token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// Two issuers in one deployment — the staff identity provider and a customer
// one, say — each verified against its own keys.
func TestJWTVerifiesTwoIssuersInOneDeployment(t *testing.T) {
	staff, customer := newIssuer(t), newIssuer(t)
	j, err := auth.NewJWT(context.Background(), []auth.Issuer{
		{URL: staff.server.URL, Audience: "audit"},
		{URL: customer.server.URL, Audience: "audit"},
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	for name, is := range map[string]*issuer{"staff": staff, "customer": customer} {
		p, err := j.Principal(context.Background(), bearer(is.token(t, is.server.URL, nil)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p.Issuer != is.server.URL || p.Subject != "u-1" || p.Via != "oidc" {
			t.Fatalf("%s: principal %+v", name, p)
		}
		if got := p.Claims["groups"]; len(got) != 1 || got[0] != "all:audit:auditor" {
			t.Fatalf("%s: groups %v", name, got)
		}
	}
}

func TestJWTRejects(t *testing.T) {
	trusted, other := newIssuer(t), newIssuer(t)
	j, err := auth.NewJWT(context.Background(), []auth.Issuer{
		{URL: trusted.server.URL, Audience: "audit"},
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		token string
	}{
		{"no token at all", ""},
		{"an issuer nobody trusts", other.token(t, other.server.URL, nil)},
		// The one that matters most: a token that names the trusted issuer
		// and is signed with somebody else's key. Choosing keys by the iss
		// claim is only safe because the chosen verifier then checks the
		// signature against them.
		{"the trusted issuer's name on another's signature", other.token(t, trusted.server.URL, nil)},
		{"a token for another service", trusted.token(t, trusted.server.URL, func(b *jwt.Builder) {
			b.Audience([]string{"billing"})
		})},
		{"an expired token", trusted.token(t, trusted.server.URL, func(b *jwt.Builder) {
			b.IssuedAt(time.Now().Add(-2 * time.Hour)).Expiration(time.Now().Add(-time.Hour))
		})},
		{"a token with no subject", trusted.token(t, trusted.server.URL, func(b *jwt.Builder) {
			b.Subject("")
		})},
		{"not a token", "not.a.token"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			if c.token != "" {
				r.Header.Set("Authorization", "Bearer "+c.token)
			}
			p, err := j.Principal(context.Background(), r)
			if !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("want ErrUnauthenticated, got %v and %+v", err, p)
			}
			// And the caller learns nothing about why.
			if err.Error() != auth.ErrUnauthenticated.Error() {
				t.Fatalf("the error tells the caller too much: %v", err)
			}
		})
	}
}

func TestJWTNeedsAnAudience(t *testing.T) {
	is := newIssuer(t)
	_, err := auth.NewJWT(context.Background(), []auth.Issuer{{URL: is.server.URL}}, quiet())
	if err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("an issuer without an audience was accepted: %v", err)
	}
}

// With two issuers, one of them a customer's, a rule that matches a group from
// anyone is operator access for whoever administers the customer's provider.
// The rule has to say which issuer it trusts that group from.
func TestARuleFromOneIssuerIsNotSatisfiedByAnother(t *testing.T) {
	staff, customer := newIssuer(t), newIssuer(t)
	j, err := auth.NewJWT(context.Background(), []auth.Issuer{
		{URL: staff.server.URL, Audience: "audit"},
		{URL: customer.server.URL, Audience: "audit"},
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	rules := auth.Declarative{Rules: []auth.Rule{{
		Name: "auditors", Issuer: staff.server.URL, Claim: "groups", Value: "all:audit:auditor",
		Grant: auth.Grant{AllTenants: true, Profiles: []string{"security"},
			Operations: []auth.Operation{auth.Search}},
	}}}
	if err := rules.BoundTo([]string{staff.server.URL, customer.server.URL}); err != nil {
		t.Fatal(err)
	}

	fromStaff, err := j.Principal(context.Background(), bearer(staff.token(t, staff.server.URL, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rules.Grant(context.Background(), fromStaff); err != nil {
		t.Fatalf("the staff auditor was refused: %v", err)
	}

	// The customer's provider asserts exactly the same group.
	fromCustomer, err := j.Principal(context.Background(), bearer(customer.token(t, customer.server.URL, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if g, err := rules.Grant(context.Background(), fromCustomer); !errors.Is(err, auth.ErrDenied) {
		t.Fatalf("a customer's provider granted itself operator access: %+v", g)
	}

	// And a rule set that forgets to say is refused before it can serve.
	loose := auth.Declarative{Rules: []auth.Rule{{Name: "auditors", Claim: "groups", Value: "all:audit:auditor"}}}
	if err := loose.BoundTo([]string{staff.server.URL, customer.server.URL}); err == nil {
		t.Fatal("a rule naming no issuer was accepted with two issuers trusted")
	}
	if err := loose.BoundTo([]string{staff.server.URL}); err != nil {
		t.Fatalf("with one issuer an unnamed rule should mean that issuer: %v", err)
	}
}
