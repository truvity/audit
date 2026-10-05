package authn_test

import (
	"testing"

	"github.com/truvity/audit/authn"
)

// The cases are those of sluis's TestSplitGroupReadsAnyGrant (policy_test.go,
// v1.63.0), with the one sluis constant it names (`all:access-roster:operator`)
// spelled out, and the answers it expects. This module does not require sluis, so
// the proof that the two readers agree is this table: when sluis's changes, so
// does this, and a reader that drifts fails here before it grants differently.
func TestSplitGroupAgreesWithSluis(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		group                  string
		ok                     bool
		scope, thing, wantRole string
	}{
		{"sluis's own", "all:access-roster:operator", true, "all", "access-roster", "operator"},
		{"a cluster role", "mgmt:k8s:admin", true, "mgmt", "k8s", "admin"},
		{"a project role", "prod:shop:deployer", true, "prod", "shop", "deployer"},
		{"another relying party, tenant-scoped", "C0north:audit:viewer", true, "C0north", "audit", "viewer"},
		{"another relying party, installation-wide", "all:audit:auditor", true, "all", "audit", "auditor"},
		{"a rung", "rung:sre", false, "", "", ""},
		{"an employee", "emp:otsar", false, "", "", ""},
		{"an ordinary name", "platform", false, "", "", ""},
		{"an address", "team@north.example", false, "", "", ""},
		{"an empty scope", ":access-roster:operator", false, "", "", ""},
		{"an empty role", "mgmt:k8s:", false, "", "", ""},
		{"four segments", "mgmt:k8s:admin:extra", false, "", "", ""},
		// What the table above does not say but the code does, so that a change to
		// either side is a decision: no trimming, no case folding, no escapes.
		{"an empty name", "", false, "", "", ""},
		{"only separators", "::", false, "", "", ""},
		{"an empty thing", "acme::viewer", false, "", "", ""},
		{"a space is a character", " acme:audit:viewer ", true, " acme", "audit", "viewer "},
		{"case is kept", "ACME:Audit:Viewer", true, "ACME", "Audit", "Viewer"},
		{"a tenant with a dash and dots", "acme-1.eu:audit:viewer", true, "acme-1.eu", "audit", "viewer"},
	} {
		scope, thing, role, ok := authn.SplitGroup(tc.group)
		if ok != tc.ok || scope != tc.scope || thing != tc.thing || role != tc.wantRole {
			t.Errorf("%s: %q → (%q, %q, %q, %v), want (%q, %q, %q, %v)",
				tc.name, tc.group, scope, thing, role, ok, tc.scope, tc.thing, tc.wantRole, tc.ok)
		}
	}
	if authn.ScopeAll != "all" || authn.GroupSeparator != ":" {
		t.Errorf("the constants are %q and %q: sluis has \"all\" and \":\"", authn.ScopeAll, authn.GroupSeparator)
	}
}
