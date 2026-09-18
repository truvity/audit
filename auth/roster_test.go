package auth_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/truvity/audit/auth"
)

// A deployment as the profiles usually look: named for what they are, built
// from the presets that say what they are.
func deployment() map[string][]string {
	return map[string][]string{
		"security":   {"security", "dora"},
		"history":    {"history"},
		"billing-nl": {"billing-nl"},
		"evidence":   {"evidence-etsi"},
		"cardholder": {"security", "pci-dss"},
	}
}

func roster() auth.AccessRoster { return auth.AccessRoster{Profiles: deployment()} }

func ops(o ...auth.Operation) []auth.Operation { return o }

// One case per role shape, which is the issue's exit for the preset.
func TestEachRoleShape(t *testing.T) {
	for _, c := range []struct {
		group    string
		tenants  []string
		all      bool
		profiles []string
		ops      []auth.Operation
	}{
		{"acme:audit:viewer", []string{"acme"}, false, []string{"history"}, ops(auth.Search, auth.Facets, auth.Get)},
		{"all:audit:security", nil, true, []string{"cardholder", "security"},
			ops(auth.Search, auth.Facets, auth.Get, auth.Tail, auth.Export)},
		{"acme:audit:security", []string{"acme"}, false, []string{"cardholder", "security"},
			ops(auth.Search, auth.Facets, auth.Get, auth.Tail, auth.Export)},
		{"all:audit:auditor", nil, true, []string{"cardholder", "evidence", "history", "security"},
			ops(auth.Search, auth.Facets, auth.Get, auth.Export)},
		{"all:audit:billing", nil, true, []string{"billing-nl"}, ops(auth.Search, auth.Facets, auth.Get, auth.Export)},
		{"all:audit:evidence", nil, true, []string{"evidence"}, ops(auth.Search, auth.Get, auth.Export)},
	} {
		t.Run(c.group, func(t *testing.T) {
			held := roster().Grants(person("u", c.group))
			if len(held) != 1 {
				t.Fatalf("%d grants, want one: %+v", len(held), held)
			}
			g := held[0]
			want := auth.Grant{Rule: c.group, Tenants: c.tenants, AllTenants: c.all, Profiles: c.profiles, Operations: c.ops}
			if !reflect.DeepEqual(g, want) {
				t.Fatalf("\n got %+v\nwant %+v", g, want)
			}
		})
	}
}

// What a group name must never do.
func TestWhatNoGroupNameGrants(t *testing.T) {
	for name, group := range map[string]string{
		// Every tenant's history is not a role anybody holds by name.
		"an installation-wide viewer": "all:audit:viewer",
		// Resolve is an explicit rule naming a person, never a group.
		"a resolver":                 "all:audit:resolver",
		"a role that does not exist": "all:audit:owner",
		// Another relying party's grant, whatever its role is called.
		"another thing":      "all:k8s:auditor",
		"a two-segment name": "emp:otsar",
		"an empty scope":     ":audit:auditor",
	} {
		t.Run(name, func(t *testing.T) {
			if held := roster().Grants(person("u", group)); len(held) != 0 {
				t.Fatalf("%q granted %+v", group, held)
			}
		})
	}
	// A role with nothing to cover in this deployment grants nothing rather
	// than a grant over no profile.
	thin := auth.AccessRoster{Profiles: map[string][]string{"history": {"history"}}}
	if held := thin.Grants(person("u", "all:audit:billing")); len(held) != 0 {
		t.Fatalf("a billing role in a deployment without billing granted %+v", held)
	}
}

// The preset sits beside explicit rules, and both flow through Effective:
// several audit groups union on a profile, and a rule adds to them.
func TestThePresetCombinesWithRules(t *testing.T) {
	d := auth.Declarative{
		Presets: []auth.Preset{roster()},
		Rules: []auth.Rule{{
			Name: "dpo-resolve", Claim: "sub", Value: "dpo",
			Grant: auth.Grant{AllTenants: true, Profiles: []string{"security"}, Operations: ops(auth.Resolve)},
		}},
	}
	ctx := context.Background()

	dpo := person("dpo", "acme:audit:viewer", "globex:audit:viewer", "all:audit:security")
	dpo.Claims["sub"] = []string{"dpo"}
	held, err := d.Grants(ctx, dpo)
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 4 {
		t.Fatalf("%d grants, want three groups and one rule: %+v", len(held), held)
	}
	g, err := auth.Effective(held, "history", auth.Search)
	if err != nil {
		t.Fatal(err)
	}
	if g.AllTenants || !reflect.DeepEqual(g.Tenants, []string{"acme", "globex"}) ||
		g.Rule != "acme:audit:viewer,globex:audit:viewer" {
		t.Fatalf("two scoped viewers: %+v", g)
	}
	if g, err = auth.Effective(held, "security", auth.Resolve); err != nil || g.Rule != "dpo-resolve" {
		t.Fatalf("the explicit rule did not add resolve: %+v %v", g, err)
	}
	if _, err := auth.Effective(held, "history", auth.Resolve); !errors.Is(err, auth.ErrDenied) {
		t.Fatal("resolve leaked from the rule's profile to another")
	}

	// A caller holding audit groups from the wrong issuer holds nothing.
	bound := auth.Declarative{Presets: []auth.Preset{auth.AccessRoster{From: "https://staff", Profiles: deployment()}}}
	if err := bound.BoundTo([]string{"https://staff", "https://customers"}); err != nil {
		t.Fatal(err)
	}
	other := person("u", "all:audit:auditor")
	other.Issuer = "https://customers"
	if _, err := bound.Grants(ctx, other); !errors.Is(err, auth.ErrDenied) {
		t.Fatalf("the customers' issuer granted an auditor: %v", err)
	}
	loose := auth.Declarative{Presets: []auth.Preset{roster()}}
	if err := loose.BoundTo([]string{"https://staff", "https://customers"}); err == nil {
		t.Fatal("a preset naming no issuer was accepted with two trusted")
	}
}
