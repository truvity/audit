// Package auth is where a deployment plugs in who is asking and what they may
// see.
//
// The two are separate on purpose. Authentication is about issuers, tokens and
// gateways, and every deployment has its own; authorization is about tenants
// and profiles, and is the same question everywhere. A component that knew
// about issuers would have to be changed to admit a new one.
//
// Both interfaces are public because a deployment implements them. The query
// service behind them is not.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Principal is who is asking, as an authenticator established it.
type Principal struct {
	// Issuer and Subject identify the caller. Subject alone is not enough:
	// two issuers may both have a subject "1", and they are not the same
	// person.
	Issuer  string
	Subject string
	// Claims are whatever the authenticator read, for the authorizer to map.
	Claims map[string][]string
	// Via records how the caller was authenticated, and is stamped into the
	// record of every read. "who read the audit log" is a poor answer without
	// it: a subject from a gateway and the same subject from a bearer token
	// are different assurances.
	Via string
}

// Authenticator establishes who is asking.
type Authenticator interface {
	Principal(ctx context.Context, req *http.Request) (Principal, error)
}

// Operation is something a caller may be allowed to do.
type Operation string

// The operations a grant may carry. Resolve — mapping a pseudonym back to a
// person — is its own operation rather than part of get, because it is the one
// that undoes the pseudonymisation and should be granted to as few roles as
// possible.
const (
	Search  Operation = "search"
	Facets  Operation = "facets"
	Get     Operation = "get"
	Export  Operation = "export"
	Tail    Operation = "tail"
	Resolve Operation = "resolve"
)

// Grant is what a principal may see. Its zero value grants nothing.
//
// The design sketch had a nil tenant list mean every tenant. That is the wrong
// default for this: a Grant that was never filled in would then be a grant over
// the whole archive, and the mistake would look like an empty struct rather
// than like a decision. Every tenant is something an operator's grant says out
// loud.
type Grant struct {
	// AllTenants is an operator's grant. Tenants is everyone else's.
	AllTenants bool
	Tenants    []string
	Profiles   []string
	Operations []Operation
	// From and Until bound what may be read. Zero means unbounded at that end.
	From, Until time.Time
	// Rule names what granted this, and is stamped into the record of the read.
	// A grant nobody can trace to a rule is one nobody can review.
	Rule string
}

// Authorizer decides what a principal may see.
//
// It answers with every grant the principal holds rather than one, because a
// person holds several — a scoped viewer role over one tenant and a security
// role over every tenant, say — and no single Grant can say both without
// saying more than either. Which of them applies is decided per request, by
// Effective, against the profile and operation actually asked for.
type Authorizer interface {
	Grants(ctx context.Context, p Principal) ([]Grant, error)
}

// Effective is the one grant that covers a request, computed from everything
// the caller holds.
//
// Only grants that allow this profile and operation count; the others say
// nothing about it. Of those, the tenants are the union, because each grant
// allows this profile over its own tenants and the union widens nothing
// beyond what each already allowed on its own. A union across profiles would
// — a viewer of one tenant's history plus a security role over every
// tenant's security profile must not become every tenant's history — which
// is why the profile is fixed first.
//
// A time window does not union. A grant with none covers every period, so if
// any covering grant is unbounded the answer is. If every one is bounded they
// have to agree: the hull of two different windows includes the gap between
// them, and which period was meant is not this service's to guess, so it
// refuses.
//
// The rule stamped on the read is every covering grant's, so that a record
// of the read says which grants together allowed it.
func Effective(grants []Grant, profile string, op Operation) (Grant, error) {
	var covering []Grant
	sawProfile := false
	for _, g := range grants {
		if !contains(g.Profiles, profile) {
			continue
		}
		sawProfile = true
		if !g.Allows(profile, op) || (!g.AllTenants && len(g.Tenants) == 0) {
			continue
		}
		covering = append(covering, g)
	}
	switch {
	case len(covering) > 0:
	case !sawProfile:
		return Grant{}, fmt.Errorf("%w: no grant includes profile %s", ErrDenied, profile)
	default:
		return Grant{}, fmt.Errorf("%w: no grant on profile %s includes %s over any tenant", ErrDenied, profile, op)
	}

	out := Grant{Profiles: []string{profile}, Operations: []Operation{op}}
	tenants := map[string]bool{}
	rules := make([]string, 0, len(covering))
	bounded, unbounded := 0, false
	for _, g := range covering {
		if g.AllTenants {
			out.AllTenants = true
		}
		for _, t := range g.Tenants {
			tenants[t] = true
		}
		if g.Rule != "" {
			rules = append(rules, g.Rule)
		}
		if g.From.IsZero() && g.Until.IsZero() {
			unbounded = true
			continue
		}
		if bounded > 0 && (!g.From.Equal(out.From) || !g.Until.Equal(out.Until)) {
			return Grant{}, fmt.Errorf(
				"%w: two grants on profile %s carry different time windows, and which period is "+
					"meant is not this service's to guess", ErrDenied, profile)
		}
		out.From, out.Until = g.From, g.Until
		bounded++
	}
	if unbounded {
		out.From, out.Until = time.Time{}, time.Time{}
	}
	if !out.AllTenants {
		for t := range tenants {
			out.Tenants = append(out.Tenants, t)
		}
		sort.Strings(out.Tenants)
	}
	sort.Strings(rules)
	out.Rule = strings.Join(rules, ",")
	return out, nil
}

// ErrDenied is returned when nothing grants the caller anything.
var ErrDenied = errors.New("auth: denied")

// Allows reports whether a grant covers a profile and an operation.
func (g Grant) Allows(profile string, op Operation) bool {
	if !contains(g.Profiles, profile) {
		return false
	}
	for _, have := range g.Operations {
		if have == op {
			return true
		}
	}
	return false
}

// TenantFilter is the tenant list a query must be narrowed to, and whether
// narrowing is needed at all.
//
// The query service passes this straight into the search as one more term. It
// is not a check performed beside the query: a check can be forgotten, and a
// term cannot be, because without it there is no query.
func (g Grant) TenantFilter() []string {
	if g.AllTenants {
		return nil
	}
	return g.Tenants
}

// Check returns an error naming what was refused, so that a caller is told
// which of the three it failed rather than a bare no.
func (g Grant) Check(profile string, op Operation) error {
	if !contains(g.Profiles, profile) {
		return fmt.Errorf("%w: this grant does not include profile %s", ErrDenied, profile)
	}
	for _, have := range g.Operations {
		if have == op {
			if !g.AllTenants && len(g.Tenants) == 0 {
				return fmt.Errorf("%w: this grant names no tenant", ErrDenied)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: this grant does not include %s", ErrDenied, op)
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// None authenticates nobody, for tests and for a deployment that puts its own
// authentication entirely in front.
type None struct {
	// As is the principal every request gets. A zero value is a caller with no
	// identity at all, which every authorizer here refuses.
	As Principal
}

// Principal implements Authenticator.
func (n None) Principal(context.Context, *http.Request) (Principal, error) {
	p := n.As
	if p.Via == "" {
		p.Via = "none"
	}
	return p, nil
}

// Declarative maps claim values to grants, from configuration.
//
// It is the default because the alternative — asking a policy engine — is a
// dependency a deployment should choose rather than inherit, and because a
// mapping in a file is something an auditor can read.
type Declarative struct {
	// Rules are explicit grants. Every rule that matches contributes; there
	// is no ordering and no first match, because Effective decides per
	// request which of a caller's grants apply and a rule that lost to an
	// earlier one would have been a grant the file says exists and the
	// service ignores.
	Rules []Rule
	// Presets derive grants from a claim's vocabulary rather than from one
	// value each: an installation whose groups already say who may read what
	// does not restate every group as a rule.
	Presets []Preset
}

// Preset derives grants from a principal's claims by a convention rather than
// by listing values.
type Preset interface {
	// Grants returns what the principal holds under this convention, and
	// nothing for a principal it does not recognise.
	Grants(p Principal) []Grant
	// Issuer is the one issuer this preset reads, or "" for any — which is
	// allowed only while one issuer is trusted, like a rule.
	Issuer() string
	// Name is what the preset is called in configuration and in messages.
	Name() string
}

// Rule maps a claim value to a grant.
type Rule struct {
	// Name is stamped into the record of every read this rule allowed.
	Name string
	// Issuer, when set, is the only issuer whose principals this rule matches.
	//
	// With one trusted issuer it can be left empty. With several it cannot,
	// and the service refuses to start if a rule omits it: every issuer can
	// assert any claim it likes, so a rule matching groups=all:audit:auditor
	// from anyone would hand operator access to whoever administers the least
	// trusted of them — a customer's own identity provider, say.
	Issuer string
	// Claim and Value are what must be present. An empty Claim matches any
	// authenticated principal, which is how a deployment writes a rule for
	// "anyone who got this far".
	Claim string
	Value string
	Grant Grant
}

// Grants implements Authorizer.
func (d Declarative) Grants(_ context.Context, p Principal) ([]Grant, error) {
	if p.Subject == "" {
		return nil, fmt.Errorf("%w: the caller has no identity", ErrDenied)
	}
	var out []Grant
	for _, r := range d.Rules {
		if !r.matches(p) {
			continue
		}
		g := r.Grant
		if g.Rule == "" {
			g.Rule = r.Name
		}
		out = append(out, g)
	}
	for _, preset := range d.Presets {
		if is := preset.Issuer(); is != "" && is != p.Issuer {
			continue
		}
		out = append(out, preset.Grants(p)...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: nothing grants %s/%s anything", ErrDenied, p.Issuer, p.Subject)
	}
	return out, nil
}

func (r Rule) matches(p Principal) bool {
	if r.Issuer != "" && r.Issuer != p.Issuer {
		return false
	}
	if r.Claim == "" {
		return true
	}
	for _, v := range p.Claims[r.Claim] {
		if v == r.Value {
			return true
		}
	}
	return false
}

// BoundTo checks that every rule names one of the given issuers, which a
// deployment trusting more than one issuer must do; see Rule.Issuer. With a
// single issuer an unnamed rule is allowed and means that issuer.
func (d Declarative) BoundTo(issuers []string) error {
	known := map[string]bool{}
	for _, is := range issuers {
		known[is] = true
	}
	for _, preset := range d.Presets {
		switch is := preset.Issuer(); {
		case is == "" && len(issuers) > 1:
			return fmt.Errorf("auth: preset %s names no issuer, and with %d trusted issuers it would "+
				"read a claim any of them asserts; say which issuer it is for", preset.Name(), len(issuers))
		case is != "" && !known[is]:
			return fmt.Errorf("auth: preset %s is for issuer %s, which is not trusted", preset.Name(), is)
		}
	}
	for _, r := range d.Rules {
		switch {
		case r.Issuer == "" && len(issuers) > 1:
			return fmt.Errorf("auth: rule %q names no issuer, and with %d trusted issuers it would "+
				"match a claim any of them asserts; say which issuer it is for", r.Name, len(issuers))
		case r.Issuer != "" && !known[r.Issuer]:
			return fmt.Errorf("auth: rule %q is for issuer %s, which is not trusted, so it can "+
				"never match", r.Name, r.Issuer)
		}
	}
	return nil
}

// Describe renders the rules, so that `audit` can print what a deployment
// grants without anybody reading the configuration format.
func (d Declarative) Describe() string {
	var b strings.Builder
	for _, r := range d.Rules {
		who := "any authenticated caller"
		if r.Claim != "" {
			who = r.Claim + "=" + r.Value
		}
		if r.Issuer != "" {
			who += " from " + r.Issuer
		}
		tenants := strings.Join(r.Grant.Tenants, ",")
		if r.Grant.AllTenants {
			tenants = "every tenant"
		}
		ops := make([]string, 0, len(r.Grant.Operations))
		for _, o := range r.Grant.Operations {
			ops = append(ops, string(o))
		}
		sort.Strings(ops)
		fmt.Fprintf(&b, "%s: %s -> profiles [%s] tenants [%s] may %s\n",
			r.Name, who, strings.Join(r.Grant.Profiles, ","), tenants, strings.Join(ops, ","))
	}
	return b.String()
}
