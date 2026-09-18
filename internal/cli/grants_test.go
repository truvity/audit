package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func grantsFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "grants.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAccessReadsIssuersAndRules(t *testing.T) {
	access, err := LoadAccess(grantsFile(t, `
issuers:
  - url: https://staff.example
    audience: audit
  - url: https://customers.example
    audience: audit
rules:
  - name: auditors
    issuer: https://staff.example
    claim: groups
    value: all:audit:auditor
    grant:
      all_tenants: true
      profiles: [security]
      operations: [search, get]
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(access.Issuers) != 2 || access.Issuers[1].Audience != "audit" {
		t.Fatalf("issuers: %+v", access.Issuers)
	}
	if r := access.Rules.Rules[0]; r.Issuer != "https://staff.example" || len(r.Grant.Operations) != 2 {
		t.Fatalf("rule: %+v", r)
	}
}

func TestLoadAccessRefuses(t *testing.T) {
	for _, c := range []struct{ name, body, says string }{{
		// Two issuers and a rule that does not say which: whoever administers
		// the customers' provider could assert the group and read everything.
		name: "a rule naming no issuer when two are trusted",
		body: `
issuers:
  - {url: https://staff.example, audience: audit}
  - {url: https://customers.example, audience: audit}
rules:
  - name: auditors
    claim: groups
    value: all:audit:auditor
    grant: {all_tenants: true, profiles: [security], operations: [search]}
`,
		says: "names no issuer",
	}, {
		name: "a rule for an issuer that is not trusted",
		body: `
issuers:
  - {url: https://staff.example, audience: audit}
rules:
  - name: auditors
    issuer: https://stafff.example
    grant: {all_tenants: true, profiles: [security], operations: [search]}
`,
		says: "not trusted",
	}, {
		// A misspelt operation used to load and grant nothing, which looks
		// like a working rule until somebody is refused.
		name: "an operation that does not exist",
		body: `
rules:
  - name: auditors
    grant: {all_tenants: true, profiles: [security], operations: [serach]}
`,
		says: "not an operation",
	}} {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadAccess(grantsFile(t, c.body))
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("want a refusal saying %q, got %v", c.says, err)
			}
		})
	}
}
