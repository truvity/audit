package auth_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v4/jwt"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/internal/authtest"
)

// groups gives a token the auditor group, then whatever else mutate says.
func groups(mutate func(*jwt.Builder)) func(*jwt.Builder) {
	return func(b *jwt.Builder) {
		b.Claim("groups", []string{"all:audit:auditor"})
		if mutate != nil {
			mutate(b)
		}
	}
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
	staff, customer := authtest.NewIssuer(t), authtest.NewIssuer(t)
	j, err := auth.NewJWT(context.Background(), []auth.Issuer{
		{URL: staff.URL, Audience: "audit"},
		{URL: customer.URL, Audience: "audit"},
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	for name, is := range map[string]*authtest.Issuer{"staff": staff, "customer": customer} {
		p, err := j.Principal(context.Background(), bearer(is.Token(t, is.URL, "u-1", groups(nil))))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p.Issuer != is.URL || p.Subject != "u-1" || p.Via != "oidc" {
			t.Fatalf("%s: principal %+v", name, p)
		}
		if got := p.Claims["groups"]; len(got) != 1 || got[0] != "all:audit:auditor" {
			t.Fatalf("%s: groups %v", name, got)
		}
	}
}

func TestJWTRejects(t *testing.T) {
	trusted, other := authtest.NewIssuer(t), authtest.NewIssuer(t)
	j, err := auth.NewJWT(context.Background(), []auth.Issuer{
		{URL: trusted.URL, Audience: "audit"},
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		token string
	}{
		{"no token at all", ""},
		{"an issuer nobody trusts", other.Token(t, other.URL, "u-1", groups(nil))},
		// The one that matters most: a token that names the trusted issuer
		// and is signed with somebody else's key. Choosing keys by the iss
		// claim is only safe because the chosen verifier then checks the
		// signature against them.
		{"the trusted issuer's name on another's signature", other.Token(t, trusted.URL, "u-1", groups(nil))},
		{"a token for another service", trusted.Token(t, trusted.URL, "u-1", func(b *jwt.Builder) {
			b.Audience([]string{"billing"})
		})},
		{"an expired token", trusted.Token(t, trusted.URL, "u-1", func(b *jwt.Builder) {
			b.IssuedAt(time.Now().Add(-2 * time.Hour)).Expiration(time.Now().Add(-time.Hour))
		})},
		{"a token with no subject", trusted.Token(t, trusted.URL, "u-1", func(b *jwt.Builder) {
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
	is := authtest.NewIssuer(t)
	_, err := auth.NewJWT(context.Background(), []auth.Issuer{{URL: is.URL}}, quiet())
	if err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("an issuer without an audience was accepted: %v", err)
	}
}

// With two issuers, one of them a customer's, a rule that matches a group from
// anyone is operator access for whoever administers the customer's provider.
// The rule has to say which issuer it trusts that group from.
func TestARuleFromOneIssuerIsNotSatisfiedByAnother(t *testing.T) {
	staff, customer := authtest.NewIssuer(t), authtest.NewIssuer(t)
	j, err := auth.NewJWT(context.Background(), []auth.Issuer{
		{URL: staff.URL, Audience: "audit"},
		{URL: customer.URL, Audience: "audit"},
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	rules := auth.Declarative{Rules: []auth.Rule{{
		Name: "auditors", Issuer: staff.URL, Claim: "groups", Value: "all:audit:auditor",
		Grant: auth.Grant{AllTenants: true, Profiles: []string{"security"},
			Operations: []auth.Operation{auth.Search}},
	}}}
	if err := rules.BoundTo([]string{staff.URL, customer.URL}); err != nil {
		t.Fatal(err)
	}

	fromStaff, err := j.Principal(context.Background(), bearer(staff.Token(t, staff.URL, "u-1", groups(nil))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rules.Grants(context.Background(), fromStaff); err != nil {
		t.Fatalf("the staff auditor was refused: %v", err)
	}

	// The customer's provider asserts exactly the same group.
	fromCustomer, err := j.Principal(context.Background(), bearer(customer.Token(t, customer.URL, "u-1", groups(nil))))
	if err != nil {
		t.Fatal(err)
	}
	if g, err := rules.Grants(context.Background(), fromCustomer); !errors.Is(err, auth.ErrDenied) {
		t.Fatalf("a customer's provider granted itself operator access: %+v", g)
	}

	// And a rule set that forgets to say is refused before it can serve.
	loose := auth.Declarative{Rules: []auth.Rule{{Name: "auditors", Claim: "groups", Value: "all:audit:auditor"}}}
	if err := loose.BoundTo([]string{staff.URL, customer.URL}); err == nil {
		t.Fatal("a rule naming no issuer was accepted with two issuers trusted")
	}
	if err := loose.BoundTo([]string{staff.URL}); err != nil {
		t.Fatalf("with one issuer an unnamed rule should mean that issuer: %v", err)
	}
}
