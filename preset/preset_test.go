package preset

import (
	"strings"
	"testing"
	"time"
)

func builtin(t *testing.T) map[string]*Preset {
	t.Helper()
	p, err := Builtin()
	if err != nil {
		t.Fatalf("the presets this repository ships do not load: %v", err)
	}
	return p
}

// A preset states what a framework requires. If it cannot say where it read
// that, it is an opinion wearing a citation's clothes.
func TestBuiltinPresetsAreComplete(t *testing.T) {
	presets := builtin(t)
	for _, want := range []string{"security", "billing-nl", "evidence-etsi", "history", "pci-dss", "dora", "nen-7513"} {
		p, ok := presets[want]
		if !ok {
			t.Fatalf("preset %q is missing", want)
		}
		if len(p.Citations) == 0 {
			t.Errorf("%s: no citations", want)
		}
		for _, c := range p.Citations {
			if !strings.HasPrefix(c.URL, "http") {
				t.Errorf("%s: citation %q has no usable URL", want, c.Clause)
			}
		}
		if !strings.Contains(strings.ToLower(p.Disclaimer), "not legal advice") {
			t.Errorf("%s: the disclaimer must say it is not legal advice", want)
		}
		if len(p.FieldClasses) == 0 {
			t.Errorf("%s: no field classes, so a copy under it would carry nothing", want)
		}
		if p.Retention.Policy == "" {
			t.Errorf("%s: no retention policy", want)
		}
	}
}

func TestComposeTakesTheStrictestIdentityTreatment(t *testing.T) {
	presets := builtin(t)
	// security keeps staff identifiers in clear; history drops them, showing a
	// staff actor by kind and role instead. Composed, the stricter reading
	// wins, and dropping is stricter than keeping.
	p, err := Compose(Composition{Name: "mixed", Presets: []string{"security", "history"}}, presets)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Identity[Internal]; got != Omit {
		t.Fatalf("internal treatment = %q, want the stricter %q", got, Omit)
	}
	if got := p.Identity[External]; got != Pseudonym {
		t.Fatalf("external treatment = %q, want %q", got, Pseudonym)
	}
}

func TestComposeTakesTheLongestRetention(t *testing.T) {
	presets := builtin(t)

	// PCI fixes twelve months as a floor; security defaults to the same number
	// but allows less. Composed, the floor rises.
	p, err := Compose(Composition{Name: "carded", Presets: []string{"security", "pci-dss"}}, presets)
	if err != nil {
		t.Fatal(err)
	}
	if p.Retention.MinimumDays != 365 {
		t.Fatalf("minimum retention = %d days, want the PCI floor of 365", p.Retention.MinimumDays)
	}
	if p.Review.Cadence != "daily" {
		t.Fatalf("review cadence = %q, want the more frequent %q", p.Review.Cadence, "daily")
	}

	// An after-expiry policy outlasts any fixed number of days, because what it
	// waits for has not happened yet.
	q, err := Compose(Composition{Name: "long", Presets: []string{"security", "evidence-etsi"}}, presets)
	if err != nil {
		t.Fatal(err)
	}
	if q.Retention.Policy != "after_expiry" {
		t.Fatalf("policy = %q, want after_expiry", q.Retention.Policy)
	}
	if q.Retention.YearsAfterExpiry != 7 {
		t.Fatalf("years after expiry = %d, want 7", q.Retention.YearsAfterExpiry)
	}
}

// Security needs to know who acted; billing must not. Composing them into one
// copy is the mistake the two-prefix design exists to prevent, so it is refused
// rather than silently resolved.
func TestComposeRefusesAContradiction(t *testing.T) {
	presets := builtin(t)
	_, err := Compose(Composition{Name: "everything", Presets: []string{"security", "billing-nl"}}, presets)
	if err == nil {
		t.Fatal("composing a profile that both requires and forbids the actor must be refused")
	}
	if !strings.Contains(err.Error(), "/actor") {
		t.Fatalf("the error should name the field in conflict: %v", err)
	}
}

func TestComposeRefusesAnUnknownPreset(t *testing.T) {
	if _, err := Compose(Composition{Name: "x", Presets: []string{"nope"}}, builtin(t)); err == nil {
		t.Fatal("want a refusal for an unknown preset")
	}
}

// A copy carries what its purpose justifies and nothing more, so a field named
// by no preset is dropped rather than carried by default.
func TestKeepsFieldIsDefaultDeny(t *testing.T) {
	presets := builtin(t)
	security, err := Compose(Composition{Name: "security", Presets: []string{"security"}}, presets)
	if err != nil {
		t.Fatal(err)
	}
	billing, err := Compose(Composition{Name: "billing", Presets: []string{"billing-nl"}}, presets)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		field            string
		security, billed bool
	}{
		{"/id", true, true},
		{"/occurred_at", true, true},
		{"/tenant_id", true, true},
		{"/action", true, true},
		{"/actor", true, false},
		{"/actor/id", true, false},
		{"/context", true, false},
		{"/context/client_addresses", true, false},
		{"/capture", true, false},
		{"/meter", false, true},
		{"/meter/quantity", false, true},
		{"/previous_attributes", false, false},
	} {
		if got := security.KeepsField(tc.field); got != tc.security {
			t.Errorf("security keeps %s = %v, want %v", tc.field, got, tc.security)
		}
		if got := billing.KeepsField(tc.field); got != tc.billed {
			t.Errorf("billing keeps %s = %v, want %v", tc.field, got, tc.billed)
		}
	}
}

// A listed child keeps the parent that has to carry it: a copy cannot hold
// /outcome/result without /outcome.
func TestKeepsFieldCarriesAncestors(t *testing.T) {
	billing, err := Compose(Composition{Name: "billing", Presets: []string{"billing-nl"}}, builtin(t))
	if err != nil {
		t.Fatal(err)
	}
	if !billing.KeepsField("/meter") {
		t.Fatal("a profile that keeps /meter/quantity must keep /meter")
	}
	if !billing.KeepsField("/outcome") {
		t.Fatal("a profile that requires /outcome/result must keep /outcome")
	}
}

// The billing copy is the seven-year tax record. It carries quantities, not
// people, so a property that resolves to a person never reaches it.
func TestKeepsPropertyByClassAndPII(t *testing.T) {
	presets := builtin(t)
	security, _ := Compose(Composition{Name: "security", Presets: []string{"security"}}, presets)
	billing, _ := Compose(Composition{Name: "billing", Presets: []string{"billing-nl"}}, presets)

	for _, tc := range []struct {
		class            Class
		pii              string
		security, billed bool
	}{
		{Shared, "none", true, true},
		{Audit, "none", true, false},
		{Metering, "none", false, true},
		{History, "none", false, false},
		{Audit, "identifier", true, false},
		{Metering, "identifier", false, false}, // billing forbids identifiers outright
	} {
		if got := security.KeepsProperty(tc.class, tc.pii); got != tc.security {
			t.Errorf("security keeps (%s,%s) = %v, want %v", tc.class, tc.pii, got, tc.security)
		}
		if got := billing.KeepsProperty(tc.class, tc.pii); got != tc.billed {
			t.Errorf("billing keeps (%s,%s) = %v, want %v", tc.class, tc.pii, got, tc.billed)
		}
	}
}

func TestRetainUntil(t *testing.T) {
	presets := builtin(t)
	written := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)

	billing, _ := Compose(Composition{Name: "billing", Presets: []string{"billing-nl"}}, presets)
	got := billing.RetainUntil(written, nil)
	if want := written.AddDate(0, 0, 2557); !got.Equal(want) {
		t.Fatalf("billing retains until %s, want %s (seven years)", got, want)
	}

	evidence, _ := Compose(Composition{Name: "evidence", Presets: []string{"evidence-etsi"}}, presets)
	expiry := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	got = evidence.RetainUntil(written, &expiry)
	if want := expiry.AddDate(7, 0, 0); !got.Equal(want) {
		t.Fatalf("evidence retains until %s, want seven years after expiry %s", got, want)
	}
	// An expiry is not always known when the record is written.
	got = evidence.RetainUntil(written, nil)
	if want := written.AddDate(0, 0, evidence.Retention.FallbackDays); !got.Equal(want) {
		t.Fatalf("with no expiry, evidence retains until %s, want the fallback %s", got, want)
	}
}

func TestExplainNamesWhatMatters(t *testing.T) {
	p, err := Compose(Composition{Name: "security", Presets: []string{"security"}}, builtin(t))
	if err != nil {
		t.Fatal(err)
	}
	out := p.Explain()
	for _, want := range []string{"profile security", "identity", "retention", "integrity", "/actor"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain does not mention %q:\n%s", want, out)
		}
	}
}

func TestLoadRefusesAPresetWithoutCitations(t *testing.T) {
	_, err := Load([]byte(`
name: bare
framework: something
version: "1"
disclaimer: This preset is not legal advice.
citations: []
field_classes: [shared]
required_fields: ["/id"]
identity: {internal: clear, external: pseudonym, machine: clear}
retention: {policy: fixed, days: 30}
integrity: {digest: required}
`))
	if err == nil {
		t.Fatal("a preset with no citations must be refused")
	}
}

func TestLoadRefusesAnUnknownKey(t *testing.T) {
	_, err := Load([]byte(`
name: typo
framework: something
version: "1"
disclaimer: This preset is not legal advice.
citations: [{clause: "x", url: "https://example.test"}]
field_classes: [shared]
required_fields: ["/id"]
retantion: {policy: fixed, days: 30}
identity: {internal: clear, external: pseudonym, machine: clear}
retention: {policy: fixed, days: 30}
integrity: {digest: required}
`))
	if err == nil {
		t.Fatal("a misspelled key must be refused, not ignored")
	}
}
