package auth

import (
	"sort"
	"strings"

	"github.com/truvity/access-roster/policy"
)

// AccessRoster reads grants out of access-roster's group vocabulary.
//
// Every grant there is named `<scope>:<thing>:<role>` — role, on thing, in
// scope — and this preset reads the ones whose thing is "audit". The scope is
// "all" or an audit tenant identifier, byte for byte; an environment is not in
// the name, because each deployment's query service requires its own token
// audience and the issuer decides who may hold which, which is where the
// estate already keeps that boundary.
//
// A role grants operations over the profiles built from certain presets, so
// that a deployment's own profile names need no mention: a profile composed
// from `security` and `dora` is a security profile whatever it is called.
//
// Two roles are deliberately absent. `resolve` comes from no group name; it
// undoes the pseudonymisation and is granted by an explicit rule naming the
// person. And there is no assessor role, because a group name carries no
// dates; a time-boxed grant is an explicit rule with a window.
type AccessRoster struct {
	// From is the issuer whose groups are read, or "" for any while one
	// issuer is trusted.
	From string
	// Claim is where the groups are. Empty means "groups".
	Claim string
	// Profiles is the deployment's profiles by name, each with the presets it
	// was composed from. It is what turns a role into profile names.
	Profiles map[string][]string
}

// Thing is what an audit log is called in the thing position of a grant.
const Thing = "audit"

// The roles, and what each grants.
var roles = map[string]role{
	// A tenant administrator or a support engineer following what happened
	// in one tenant. It must be scoped: every tenant's history is not a role
	// anybody holds by group name.
	"viewer": {
		presets: []string{"history"},
		ops:     []Operation{Search, Facets, Get},
		scoped:  true,
	},
	// Security operations, incident response and the add-ons that raise a
	// security profile's minimums.
	"security": {
		presets: []string{"security", "dora", "pci-dss", "nen-7513"},
		ops:     []Operation{Search, Facets, Get, Tail, Export},
	},
	// Internal or ISMS audit: everything but the money.
	"auditor": {
		except: "billing-",
		ops:    []Operation{Search, Facets, Get, Export},
	},
	// Finance and invoice disputes: the money and nothing else.
	"billing": {
		prefix: "billing-",
		ops:    []Operation{Search, Facets, Get, Export},
	},
	// Legal and a conformity assessment body. No facets: evidence is read,
	// not summarised.
	"evidence": {
		presets: []string{"evidence-etsi"},
		ops:     []Operation{Search, Get, Export},
	},
}

type role struct {
	// presets names the presets a profile is built from to count; prefix
	// matches presets by name prefix; except matches every profile built
	// from no preset with that prefix. One of the three is set.
	presets []string
	prefix  string
	except  string
	ops     []Operation
	// scoped is a role that grants nothing installation-wide.
	scoped bool
}

// profiles is the deployment's profiles this role covers, sorted.
func (r role) profiles(all map[string][]string) []string {
	var out []string
	for name, presets := range all {
		if r.covers(presets) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (r role) covers(presets []string) bool {
	if r.except != "" {
		for _, p := range presets {
			if strings.HasPrefix(p, r.except) {
				return false
			}
		}
		return len(presets) > 0
	}
	for _, p := range presets {
		if r.prefix != "" && strings.HasPrefix(p, r.prefix) {
			return true
		}
		for _, want := range r.presets {
			if p == want {
				return true
			}
		}
	}
	return false
}

// Name implements Preset.
func (AccessRoster) Name() string { return "access-roster" }

// Issuer implements Preset.
func (a AccessRoster) Issuer() string { return a.From }

// Grants implements Preset.
//
// Each audit group the principal holds becomes one grant, named for the
// group, so that the record of a read says `acme:audit:viewer` rather than a
// rule name somebody invented. A group whose role covers none of this
// deployment's profiles grants nothing here, which is right: a billing role
// in a deployment with no billing profile has nothing to read.
func (a AccessRoster) Grants(p Principal) []Grant {
	claim := a.Claim
	if claim == "" {
		claim = "groups"
	}
	var out []Grant
	for _, name := range p.Claims[claim] {
		scope, thing, roleName, ok := policy.SplitGroup(name)
		if !ok || thing != Thing {
			continue
		}
		r, known := roles[roleName]
		if !known {
			continue
		}
		g := Grant{
			Rule:       name,
			Profiles:   r.profiles(a.Profiles),
			Operations: append([]Operation(nil), r.ops...),
		}
		if scope == policy.ScopeAll {
			if r.scoped {
				continue
			}
			g.AllTenants = true
		} else {
			g.Tenants = []string{scope}
		}
		if len(g.Profiles) == 0 {
			continue
		}
		out = append(out, g)
	}
	return out
}
