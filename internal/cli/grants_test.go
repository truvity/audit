package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/preset"
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
`), nil)
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
			_, err := LoadAccess(grantsFile(t, c.body), nil)
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("want a refusal saying %q, got %v", c.says, err)
			}
		})
	}
}

// An external assessor is given the period under assessment, not the archive.
func TestAGrantCanBeBoundedInTime(t *testing.T) {
	access, err := LoadAccess(grantsFile(t, `
issuers: [{url: https://staff.example, audience: audit}]
rules:
  - name: assessor-2026-q3
    claim: groups
    value: all:audit:assessor
    grant:
      all_tenants: true
      profiles: [security]
      operations: [search, get]
      from: 2026-07-01T00:00:00Z
      until: 2026-10-01T00:00:00Z
`), nil)
	if err != nil {
		t.Fatal(err)
	}
	g := access.Rules.Rules[0].Grant
	if g.From.Format("2006-01-02") != "2026-07-01" || g.Until.Format("2006-01-02") != "2026-10-01" {
		t.Fatalf("window %v .. %v", g.From, g.Until)
	}

	_, err = LoadAccess(grantsFile(t, `
rules:
  - name: backwards
    grant: {all_tenants: true, profiles: [security], operations: [search],
            from: 2026-10-01T00:00:00Z, until: 2026-07-01T00:00:00Z}
`), nil)
	if err == nil || !strings.Contains(err.Error(), "ends before it starts") {
		t.Fatalf("a backwards window was accepted: %v", err)
	}
}

// A preset needs the deployment's profiles, and turns its roles into whatever
// they are called here.
func TestLoadAccessWiresThePreset(t *testing.T) {
	body := `
issuers: [{url: https://staff.example, audience: audit}]
presets: [{name: access-roster}]
`
	if _, err := LoadAccess(grantsFile(t, body), nil); err == nil || !strings.Contains(err.Error(), "--deployment") {
		t.Fatalf("a preset without profiles was accepted: %v", err)
	}
	profiles := map[string]*preset.Profile{
		"sec":  {Name: "sec", Presets: []string{"security"}},
		"hist": {Name: "hist", Presets: []string{"history"}},
	}
	access, err := LoadAccess(grantsFile(t, body), profiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(access.Rules.Presets) != 1 {
		t.Fatalf("presets: %+v", access.Rules.Presets)
	}
	held := access.Rules.Presets[0].Grants(auth.Principal{
		Issuer: "https://staff.example", Subject: "u",
		Claims: map[string][]string{"groups": {"acme:audit:viewer"}},
	})
	if len(held) != 1 || len(held[0].Profiles) != 1 || held[0].Profiles[0] != "hist" {
		t.Fatalf("the viewer role did not land on the history-built profile: %+v", held)
	}
	if _, err := LoadAccess(grantsFile(t, `presets: [{name: cerbos}]`), profiles); err == nil {
		t.Fatal("an unknown preset was accepted")
	}
}
