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
type Authorizer interface {
	Grant(ctx context.Context, p Principal) (Grant, error)
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
	// Rules are tried in order and the first match wins, so a narrower rule
	// goes above a broader one. Order is the deployment's, not this code's.
	Rules []Rule
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

// Grant implements Authorizer.
func (d Declarative) Grant(_ context.Context, p Principal) (Grant, error) {
	if p.Subject == "" {
		return Grant{}, fmt.Errorf("%w: the caller has no identity", ErrDenied)
	}
	for _, r := range d.Rules {
		if !r.matches(p) {
			continue
		}
		g := r.Grant
		if g.Rule == "" {
			g.Rule = r.Name
		}
		return g, nil
	}
	return Grant{}, fmt.Errorf("%w: no rule grants %s/%s anything", ErrDenied, p.Issuer, p.Subject)
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
