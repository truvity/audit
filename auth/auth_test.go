package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/auth"
)

func rules() auth.Declarative {
	return auth.Declarative{Rules: []auth.Rule{
		{
			Name: "security-team", Claim: "groups", Value: "security",
			Grant: auth.Grant{
				AllTenants: true,
				Profiles:   []string{"security", "history"},
				Operations: []auth.Operation{auth.Search, auth.Facets, auth.Get, auth.Tail},
			},
		},
		{
			Name: "acme-support", Claim: "groups", Value: "support",
			Grant: auth.Grant{
				Tenants:    []string{"acme"},
				Profiles:   []string{"history"},
				Operations: []auth.Operation{auth.Search, auth.Get},
			},
		},
	}}
}

func person(subject string, groups ...string) auth.Principal {
	return auth.Principal{
		Issuer: "https://issuer.test", Subject: subject,
		Claims: map[string][]string{"groups": groups}, Via: "jwt",
	}
}

// The zero Grant must grant nothing. The design sketch had a nil tenant list
// mean every tenant, which would make a struct nobody filled in a grant over
// the whole archive.
func TestTheZeroGrantGrantsNothing(t *testing.T) {
	var g auth.Grant
	if g.Allows("security", auth.Search) {
		t.Fatal("an empty grant allowed a search")
	}
	if err := g.Check("security", auth.Search); err == nil {
		t.Fatal("an empty grant passed its check")
	}
	if g.TenantFilter() != nil {
		t.Fatal("an empty grant produced no tenant narrowing, which would read as every tenant")
	}
}

// Every tenant is something an operator's grant says out loud.
func TestEveryTenantIsSaidOutLoud(t *testing.T) {
	operator := auth.Grant{
		AllTenants: true, Profiles: []string{"security"},
		Operations: []auth.Operation{auth.Search},
	}
	if operator.TenantFilter() != nil {
		t.Fatal("an all-tenants grant narrowed the query anyway")
	}
	if err := operator.Check("security", auth.Search); err != nil {
		t.Fatal(err)
	}

	// A grant that names no tenant and is not an operator's grants nothing,
	// rather than silently meaning all of them.
	empty := auth.Grant{Profiles: []string{"security"}, Operations: []auth.Operation{auth.Search}}
	if err := empty.Check("security", auth.Search); err == nil {
		t.Fatal("a grant naming no tenant was accepted")
	}
}

// A refusal should say which of the three failed, so that whoever is debugging
// a permission is not guessing.
func TestARefusalSaysWhatWasMissing(t *testing.T) {
	g := auth.Grant{
		Tenants: []string{"acme"}, Profiles: []string{"history"},
		Operations: []auth.Operation{auth.Search},
	}
	for _, c := range []struct {
		name, profile string
		op            auth.Operation
		want          string
	}{
		{"wrong profile", "security", auth.Search, "profile security"},
		{"wrong operation", "history", auth.Export, "export"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := g.Check(c.profile, c.op)
			if err == nil {
				t.Fatal("want a refusal")
			}
			if !errors.Is(err, auth.ErrDenied) {
				t.Fatalf("not a denial: %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the refusal should name %q: %v", c.want, err)
			}
		})
	}
}

// Every rule that matches contributes a grant; which of them apply is decided
// per request by Effective. There is no first match: a rule that lost to an
// earlier one would be a grant the file says exists and the service ignores.
func TestEveryMatchingRuleContributes(t *testing.T) {
	d := rules()
	ctx := context.Background()

	held, err := d.Grants(ctx, person("ps_1", "support", "security"))
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 2 {
		t.Fatalf("a caller in both groups holds %d grants, want both: %+v", len(held), held)
	}
	// On history both apply, and together they cover every tenant.
	g, err := auth.Effective(held, "history", auth.Search)
	if err != nil {
		t.Fatal(err)
	}
	if !g.AllTenants || g.Rule != "acme-support,security-team" {
		t.Fatalf("history: %+v", g)
	}
	// On security only one does, and the other's tenant does not leak in.
	g, err = auth.Effective(held, "security", auth.Search)
	if err != nil {
		t.Fatal(err)
	}
	if g.Rule != "security-team" || !g.AllTenants {
		t.Fatalf("security: %+v", g)
	}

	held, err = d.Grants(ctx, person("ps_2", "support"))
	if err != nil {
		t.Fatal(err)
	}
	g, err = auth.Effective(held, "history", auth.Search)
	if err != nil {
		t.Fatal(err)
	}
	if g.Rule != "acme-support" || g.AllTenants {
		t.Fatalf("support got more than its rule: %+v", g)
	}
	if len(g.TenantFilter()) != 1 || g.TenantFilter()[0] != "acme" {
		t.Fatalf("the tenant narrowing is %v", g.TenantFilter())
	}
}

// Effective fixes the profile before it unions anything, because a union
// across profiles widens: a viewer of one tenant's history plus a security
// role over every tenant's security profile is not every tenant's history.
func TestEffectiveDoesNotUnionAcrossProfiles(t *testing.T) {
	held := []auth.Grant{
		{Rule: "acme:audit:viewer", Tenants: []string{"acme"}, Profiles: []string{"history"},
			Operations: []auth.Operation{auth.Search}},
		{Rule: "all:audit:security", AllTenants: true, Profiles: []string{"security"},
			Operations: []auth.Operation{auth.Search}},
	}
	g, err := auth.Effective(held, "history", auth.Search)
	if err != nil {
		t.Fatal(err)
	}
	if g.AllTenants || len(g.Tenants) != 1 || g.Tenants[0] != "acme" || g.Rule != "acme:audit:viewer" {
		t.Fatalf("history over every tenant: %+v", g)
	}
	if _, err := auth.Effective(held, "history", auth.Export); !errors.Is(err, auth.ErrDenied) {
		t.Fatalf("an operation no grant on that profile has was allowed: %v", err)
	}
	if _, err := auth.Effective(held, "billing", auth.Search); !errors.Is(err, auth.ErrDenied) {
		t.Fatalf("a profile no grant names was allowed: %v", err)
	}
}

// A window does not union: an unbounded grant makes the answer unbounded,
// and two different windows are refused rather than hulled.
func TestEffectiveWindows(t *testing.T) {
	q3 := auth.Grant{Rule: "q3", AllTenants: true, Profiles: []string{"security"},
		Operations: []auth.Operation{auth.Search},
		From:       time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	q1 := q3
	q1.Rule, q1.From, q1.Until = "q1", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	always := auth.Grant{Rule: "always", AllTenants: true, Profiles: []string{"security"},
		Operations: []auth.Operation{auth.Search}}

	g, err := auth.Effective([]auth.Grant{q3}, "security", auth.Search)
	if err != nil || !g.From.Equal(q3.From) || !g.Until.Equal(q3.Until) {
		t.Fatalf("one bounded grant: %+v %v", g, err)
	}
	g, err = auth.Effective([]auth.Grant{q3, always}, "security", auth.Search)
	if err != nil || !g.From.IsZero() || !g.Until.IsZero() {
		t.Fatalf("bounded plus unbounded should be unbounded: %+v %v", g, err)
	}
	if _, err := auth.Effective([]auth.Grant{q1, q3}, "security", auth.Search); !errors.Is(err, auth.ErrDenied) {
		t.Fatalf("two different windows were combined: %v", err)
	}
}

// Nothing grants a caller nothing, rather than an empty grant that would read
// as a grant.
func TestAnUnmatchedCallerIsDenied(t *testing.T) {
	_, err := rules().Grants(context.Background(), person("ps_3", "marketing"))
	if !errors.Is(err, auth.ErrDenied) {
		t.Fatalf("an unmatched caller got %v", err)
	}
}

// A caller with no identity is denied before any rule is consulted: a rule
// matching "any authenticated caller" must not match an unauthenticated one.
func TestACallerWithNoIdentityIsDenied(t *testing.T) {
	d := auth.Declarative{Rules: []auth.Rule{{
		Name: "anyone",
		Grant: auth.Grant{
			AllTenants: true, Profiles: []string{"security"},
			Operations: []auth.Operation{auth.Search},
		},
	}}}
	if _, err := d.Grants(context.Background(), auth.Principal{}); !errors.Is(err, auth.ErrDenied) {
		t.Fatalf("a principal with no subject got %v", err)
	}
	if _, err := d.Grants(context.Background(), person("ps_9")); err != nil {
		t.Fatalf("an authenticated caller was refused by a rule that matches anyone: %v", err)
	}
}

// The rule's name reaches the grant, because it is stamped into the record of
// every read: a grant nobody can trace to a rule is one nobody can review.
func TestTheRuleNamesItself(t *testing.T) {
	held, err := rules().Grants(context.Background(), person("ps_4", "support"))
	if err != nil {
		t.Fatal(err)
	}
	if g := held[0]; g.Rule == "" {
		t.Fatal("the grant does not name what granted it")
	}
	if !strings.Contains(rules().Describe(), "acme-support") {
		t.Fatal("Describe does not render the rules an auditor would read")
	}
}

// Resolve is its own operation: it undoes the pseudonymisation, so a grant that
// allows reading must not imply it.
func TestResolveIsNotImpliedByGet(t *testing.T) {
	held, err := rules().Grants(context.Background(), person("ps_5", "security"))
	if err != nil {
		t.Fatal(err)
	}
	g := held[0]
	if !g.Allows("security", auth.Get) {
		t.Fatal("the security team cannot read")
	}
	if g.Allows("security", auth.Resolve) {
		t.Fatal("a grant to read implied a grant to undo the pseudonymisation")
	}
}
