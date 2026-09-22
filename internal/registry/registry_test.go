package registry_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/internal/registry"
	"github.com/truvity/audit/preset"
)

const walletDoc = `
source: wallet
version: "1.0.0"
locales: [en]
actions:
  wallet.credential.issued:
    summary: A credential was issued.
    operation: create
    categories: [data_change]
    profiles: [security]
    message: { en: "{actor} issued a credential" }
`

// A catalogue whose actions land in a profile that requires categories it does
// not carry would let the profile look complete while missing what it is for.
const thinDoc = `
source: wallet
version: "2.0.0"
locales: [en]
actions:
  wallet.credential.issued:
    summary: A credential was issued.
    operation: create
    categories: [data_change]
    profiles: [needs-auth]
    message: { en: "{actor} issued a credential" }
`

func registryFor(t *testing.T, profiles map[string]*preset.Profile, who string) *registry.Registry {
	t.Helper()
	return &registry.Registry{
		Store:    &registry.Memory{},
		Profiles: profiles,
		Identity: func(context.Context) string { return who },
		Now:      func() time.Time { return time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC) },
	}
}

func entry(doc string) registry.Entry {
	return registry.Entry{Source: "wallet", Version: version(doc), Document: []byte(doc)}
}

func version(doc string) string {
	if strings.Contains(doc, `version: "2.0.0"`) {
		return "2.0.0"
	}
	return "1.0.0"
}

func TestRegisterAcceptsAValidCatalogue(t *testing.T) {
	r := registryFor(t, nil, "wallet")
	problems, err := r.Register(context.Background(), entry(walletDoc))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	got, err := r.Get(context.Background(), "wallet", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "wallet" {
		t.Fatalf("resolved %q", got.Source)
	}
}

// Every replica of an application registers on start-up, so registering twice
// is how a deployment rolls, not an error.
func TestRegisteringTheSameCatalogueTwiceIsFine(t *testing.T) {
	r := registryFor(t, nil, "wallet")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		problems, err := r.Register(ctx, entry(walletDoc))
		if err != nil || len(problems) != 0 {
			t.Fatalf("pass %d: %v %v", i, problems, err)
		}
	}
	all, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("%d entries for one catalogue", len(all))
	}
}

// A version says what a record written under it means. Letting one version mean
// two things would make the archive's copy and the emitter's copy disagree
// about records already written.
func TestAVersionCannotChangeUnderneath(t *testing.T) {
	r := registryFor(t, nil, "wallet")
	ctx := context.Background()
	if _, err := r.Register(ctx, entry(walletDoc)); err != nil {
		t.Fatal(err)
	}
	changed := entry(walletDoc)
	changed.Document = []byte(strings.Replace(walletDoc,
		"A credential was issued.", "Something else entirely.", 1))

	problems, err := r.Register(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 {
		t.Fatal("a version was allowed to change what it means")
	}
	if !strings.Contains(problems[0], "new version") {
		t.Errorf("the refusal should say what to do instead: %v", problems)
	}
}

// Whose catalogue this is, is not the document's to claim: a workload that
// could register under another source could describe another application's
// records, and everything downstream reads the description.
func TestOnlyTheSourceItselfMayRegister(t *testing.T) {
	r := registryFor(t, nil, "billing")
	problems, err := r.Register(context.Background(), entry(walletDoc))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 {
		t.Fatal("one source registered another's catalogue")
	}
	if !strings.Contains(problems[0], "billing may not register") {
		t.Errorf("problems: %v", problems)
	}
}

// A transport that cannot say who is calling gets a refusal, not the benefit of
// the doubt.
func TestAnUnverifiedCallerIsRefused(t *testing.T) {
	r := registryFor(t, nil, "")
	problems, err := r.Register(context.Background(), entry(walletDoc))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 || !strings.Contains(problems[0], "could not be verified") {
		t.Fatalf("problems: %v", problems)
	}
}

// Coverage is the deployment's: a gap is reported, and the application that
// registered while it was open is not refused for it.
func TestAnUncoveredCategoryIsReportedNotRefused(t *testing.T) {
	profiles := map[string]*preset.Profile{
		"needs-auth": {Name: "needs-auth", RequiredCategories: []string{"authentication", "log_access"}},
	}
	r := registryFor(t, profiles, "wallet")
	var reported []string
	r.OnUncovered = func(_ context.Context, profile string, missing []string) {
		reported = append(reported, profile+": "+strings.Join(missing, ", "))
	}
	problems, err := r.Register(context.Background(), entry(thinDoc))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("a catalogue was refused for what the deployment lacks: %v", problems)
	}
	if len(reported) != 1 || reported[0] != "needs-auth: authentication, log_access" {
		t.Fatalf("reported %q", reported)
	}
}

// What one application does not emit, another, or the component itself, may:
// coverage counts every catalogue of the deployment.
func TestCoverageCountsEveryCatalogueOfTheDeployment(t *testing.T) {
	own, err := catalogue.Load([]byte(`
source: audit
version: "1.0.0"
locales: [en]
actions:
  audit.search:
    summary: A search.
    operation: access
    categories: [log_access]
    profiles: [needs-auth]
    message: { en: "{actor} searched" }
`), nil)
	if err != nil {
		t.Fatal(err)
	}
	profiles := map[string]*preset.Profile{
		"needs-auth": {Name: "needs-auth", RequiredCategories: []string{"data_change", "log_access"}},
	}
	r := registryFor(t, profiles, "wallet")
	r.Builtin = []*catalogue.Catalogue{own}
	r.OnUncovered = func(_ context.Context, profile string, missing []string) {
		t.Errorf("%s reported uncovered: %v", profile, missing)
	}
	if problems, err := r.Register(context.Background(), entry(thinDoc)); err != nil || len(problems) != 0 {
		t.Fatalf("problems %v, err %v", problems, err)
	}
}

// The document's own source and version have to agree with what it was
// registered as, or the registry would file it under a name it does not answer
// to.
func TestTheDocumentMustAgreeWithHowItWasRegistered(t *testing.T) {
	r := registryFor(t, nil, "wallet")
	e := entry(walletDoc)
	e.Version = "9.9.9"
	problems, err := r.Register(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 {
		t.Fatal("a document was filed under a version it does not carry")
	}
}

// The writer resolves every record's catalogue through this, per record, so an
// unregistered one is an error it can dead-letter on.
func TestGetIsAnErrorForSomethingNeverRegistered(t *testing.T) {
	r := registryFor(t, nil, "wallet")
	if _, err := r.Get(context.Background(), "wallet", "3.0.0"); err == nil {
		t.Fatal("an unregistered catalogue resolved")
	}
}

// The deployment records a registration, because a catalogue arriving is a
// change to what the archive's records mean.
func TestRegisteringIsReportedOnce(t *testing.T) {
	r := registryFor(t, nil, "wallet")
	var seen []registry.Entry
	r.OnRegistered = func(_ context.Context, e registry.Entry) { seen = append(seen, e) }
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := r.Register(ctx, entry(walletDoc)); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("%d registrations reported for one catalogue registered twice", len(seen))
	}
	if seen[0].RegisteredBy != "wallet" || seen[0].RegisteredAt.IsZero() {
		t.Fatalf("the report does not say who and when: %+v", seen[0])
	}
}

// A catalogue asking for a property to be hashed is refused where a deployment
// runs no key provider. Accepting it would let the application start against an
// installation that dead-letters every record carrying that property.
func TestRegisterRefusesHashingWithNoKeys(t *testing.T) {
	r := registryFor(t, nil, "wallet")
	e := registry.Entry{
		Source: "wallet", Version: "1.0.0",
		Document: []byte(hashingDoc),
		Schemas:  map[string][]byte{hashingSchemaID: []byte(hashingSchema)},
	}

	problems, err := r.Register(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 {
		t.Fatal("a hashing catalogue was accepted by a deployment with no keys")
	}
	if !strings.Contains(strings.Join(problems, " "), "hashed") {
		t.Errorf("the problem should say what is wrong: %v", problems)
	}

	// With a provider there is nothing to warn about.
	with := registryFor(t, nil, "wallet")
	with.Keys = true
	problems, err = with.Register(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		if strings.Contains(p, "hashed") {
			t.Errorf("a deployment with keys was told about hashing: %v", problems)
		}
	}
}

const hashingSchemaID = "https://schemas.example/wallet/credential-issued/v1.json"

const hashingDoc = `
source: wallet
version: "1.0.0"
locales: [en]
actions:
  wallet.credential.issued:
    summary: A credential was issued.
    operation: create
    categories: [data_change]
    profiles: [security]
    data_schema: ` + hashingSchemaID + `
    message: { en: "{actor} issued a credential" }
`

const hashingSchema = `{
  "$id": "` + hashingSchemaID + `",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "device_id": {
      "type": "string",
      "x-audit-class": "audit",
      "x-audit-pii": "identifier",
      "x-audit-sensitive": "hmac"
    }
  }
}`
