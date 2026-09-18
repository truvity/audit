package writer_test

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/internal/writer"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/record"
)

const walletSchema = `{
  "$id": "https://schemas.example/wallet/credential-issued.json",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "credential_format": {
      "type": "string",
      "x-audit-class": "shared",
      "x-audit-pii": "none",
      "x-audit-facet": true
    },
    "reviewer_note": {
      "type": "string",
      "x-audit-class": "audit",
      "x-audit-pii": "none"
    },
    "attestation_bytes": {
      "type": "integer",
      "x-audit-class": "metering",
      "x-audit-pii": "none"
    },
    "device_id": {
      "type": "string",
      "x-audit-class": "audit",
      "x-audit-pii": "identifier",
      "x-audit-sensitive": "hmac"
    },
    "internal_note": {
      "type": "string",
      "x-audit-class": "audit",
      "x-audit-pii": "none",
      "x-audit-sensitive": "redact"
    }
  }
}`

const walletDoc = `
source: wallet
version: "1.0.0"
locales: [en]
actor_kinds:
  holder: { category: external }
  operator: { category: internal }
  issuer: { category: machine }
target_types:
  credential: { description: "An issued credential." }
  holder: { description: "A person.", is_person: true }
meters:
  issued:
    kind: count
    unit: credential
actions:
  wallet.credential.issued:
    summary: A credential was issued.
    operation: create
    categories: [credential_lifecycle, billing]
    profiles: [security, billing, history, nowhere]
    target_types: [credential, holder]
    data_schema: https://schemas.example/wallet/credential-issued.json
    message:
      en: "{actor} was issued a {data_credential_format} credential"
    meter:
      name: issued
`

func composed(t *testing.T) *catalogue.Composed {
	t.Helper()
	c, err := catalogue.Load([]byte(walletDoc), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	x, err := c.Compose("wallet.credential.issued")
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func profiles(t *testing.T) map[string]*preset.Profile {
	t.Helper()
	builtin, err := preset.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*preset.Profile{}
	for name, presets := range map[string][]string{
		"security": {"security"},
		"billing":  {"billing-nl"},
		"history":  {"history"},
	} {
		p, err := preset.Compose(preset.Composition{Name: name, Presets: presets}, builtin)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = p
	}
	return out
}

func splitter(t *testing.T) *writer.Splitter {
	t.Helper()
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatal(err)
	}
	provider, err := keys.NewLocal(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return &writer.Splitter{Profiles: profiles(t), Keys: provider}
}

func issued(t *testing.T) *record.Record {
	t.Helper()
	data, err := structpb.NewStruct(map[string]any{
		"credential_format": "sd-jwt-vc",
		"reviewer_note":     "checked by hand",
		"attestation_bytes": 2048,
		"device_id":         "device-42",
		"internal_note":     "never leaves this process",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &record.Record{
		Id:               "0199b100-0000-7000-8000-00000000cafe",
		OccurredAt:       timestamppb.Now(),
		RecordedAt:       timestamppb.Now(),
		SchemaVersion:    record.SchemaVersion,
		CatalogueVersion: "1.0.0",
		Source:           "wallet",
		Observer:         &record.Observer{Id: "wallet", Version: "1", Instance: "wallet-1"},
		Sequence:         7,
		Action:           "wallet.credential.issued",
		Operation:        auditv1.Operation_OPERATION_CREATE,
		Outcome:          &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
		TenantId:         "acme",
		Subject:          &record.Party{Kind: "holder", Id: "alice"},
		Actor:            &record.Actor{Kind: "operator", Id: "olga", SessionId: "s_1"},
		Targets: []*record.Target{
			{Type: "credential", Id: "cred-1"},
			{Type: "holder", Id: "alice"},
		},
		Context:    &record.Context{ClientAddresses: []string{"203.0.113.7"}, UserAgent: "curl"},
		Data:       data,
		Attributes: map[string]string{"how": "console"},
		Meter: &record.Meter{
			Name: "issued", Quantity: "1", Unit: "credential", Kind: auditv1.Meter_KIND_COUNT,
		},
		OriginHash: strings.Repeat("a", 64),
	}
}

func byProfile(t *testing.T, copies []*record.Record) map[string]*record.Record {
	t.Helper()
	out := map[string]*record.Record{}
	for _, c := range copies {
		out[c.GetProfile()] = c
	}
	return out
}

// The two-prefix design, in one test: the security copy knows who acted and the
// billing copy does not, and neither can be joined to the other on a person.
func TestSplitKeepsWhatEachProfileJustifies(t *testing.T) {
	s := splitter(t)
	x := composed(t)
	copies, err := s.Split(context.Background(), issued(t), x)
	if err != nil {
		t.Fatal(err)
	}
	if len(copies) != 3 {
		t.Fatalf("made %d copies, want one per configured profile the action names", len(copies))
	}
	got := byProfile(t, copies)

	security, billing := got["security"], got["billing"]
	if security == nil || billing == nil {
		t.Fatalf("copies: %v", got)
	}

	// Billing carries quantities, not people.
	if billing.GetActor() != nil {
		t.Fatalf("the billing copy carries an actor: %+v", billing.GetActor())
	}
	if billing.GetContext() != nil {
		t.Fatal("the billing copy carries the request context")
	}
	if billing.GetMeter().GetQuantity() != "1" {
		t.Fatalf("the billing copy lost the measurement: %+v", billing.GetMeter())
	}
	if billing.GetOutcome().GetResult() != auditv1.Outcome_RESULT_SUCCESS {
		t.Fatal("the billing copy lost the outcome a dispute asks about")
	}

	// Security knows who acted and from where.
	if security.GetActor().GetId() == "" {
		t.Fatal("the security copy has no actor")
	}
	if len(security.GetContext().GetClientAddresses()) == 0 {
		t.Fatal("the security copy lost the address chain")
	}
	if security.GetMeter() != nil {
		t.Fatalf("the security copy carries a meter it has no use for: %+v", security.GetMeter())
	}

	// Both descend from one original and can be shown to.
	for name, c := range got {
		if c.GetOriginHash() != strings.Repeat("a", 64) {
			t.Fatalf("the %s copy lost the origin hash, so nothing relates it to its siblings", name)
		}
		if c.GetId() != "0199b100-0000-7000-8000-00000000cafe" {
			t.Fatalf("the %s copy lost the record identifier", name)
		}
	}
}

// An internal actor is accountable by name; an external one is not, and the two
// copies must not be joinable on them.
func TestSplitAppliesTheProfilesIdentityTreatment(t *testing.T) {
	s := splitter(t)
	copies, err := s.Split(context.Background(), issued(t), composed(t))
	if err != nil {
		t.Fatal(err)
	}
	got := byProfile(t, copies)

	// Security keeps staff in clear: accountability is a legal obligation.
	if id := got["security"].GetActor().GetId(); id != "olga" {
		t.Fatalf("security actor = %q, want the identifier in clear", id)
	}
	// The subject is an end user and is pseudonymised.
	if id := got["security"].GetSubject().GetId(); !keys.IsPseudonym(id) {
		t.Fatalf("security subject = %q, want a pseudonym", id)
	}
	// History pseudonymises staff too, being the stricter reading.
	if id := got["history"].GetActor().GetId(); !keys.IsPseudonym(id) {
		t.Fatalf("history actor = %q, want a pseudonym", id)
	}
	// The same person, in two copies, under different keys.
	if got["security"].GetSubject().GetId() == got["history"].GetSubject().GetId() {
		t.Fatal("one person has the same pseudonym in two profiles; the copies could be joined")
	}
	// A session locates a person as surely as a name, so it follows the same
	// treatment as the actor it belongs to: readable where the actor is, and a
	// pseudonym where the actor is one. Pseudonymising one and not the other
	// would give a reader the person without the session, or the reverse.
	if sid := got["security"].GetActor().GetSessionId(); sid != "s_1" {
		t.Fatalf("security session = %q, want it readable beside a readable actor", sid)
	}
	if sid := got["history"].GetActor().GetSessionId(); !keys.IsPseudonym(sid) {
		t.Fatalf("history session = %q, want a pseudonym beside a pseudonymised actor", sid)
	}
}

// A target that is a person is treated like one, whatever it is called.
func TestSplitTreatsAPersonTarget(t *testing.T) {
	s := splitter(t)
	copies, err := s.Split(context.Background(), issued(t), composed(t))
	if err != nil {
		t.Fatal(err)
	}
	security := byProfile(t, copies)["security"]

	var credential, holder *record.Target
	for _, target := range security.GetTargets() {
		switch target.GetType() {
		case "credential":
			credential = target
		case "holder":
			holder = target
		}
	}
	if credential.GetId() != "cred-1" {
		t.Fatalf("a thing was pseudonymised: %+v", credential)
	}
	if !keys.IsPseudonym(holder.GetId()) {
		t.Fatalf("a person target was left in clear: %+v", holder)
	}
}

// Adding a property to a schema must not quietly widen every copy of it.
func TestSplitFiltersExtensionPropertiesByClass(t *testing.T) {
	s := splitter(t)
	copies, err := s.Split(context.Background(), issued(t), composed(t))
	if err != nil {
		t.Fatal(err)
	}
	got := byProfile(t, copies)

	security := got["security"].GetData().GetFields()
	if _, ok := security["credential_format"]; !ok {
		t.Fatal("the security copy lost a shared property")
	}
	if _, ok := security["reviewer_note"]; !ok {
		t.Fatal("the security copy lost an audit property")
	}
	if _, ok := security["attestation_bytes"]; ok {
		t.Fatal("the security copy kept a metering property it has no use for")
	}

	billing := got["billing"].GetData().GetFields()
	if _, ok := billing["attestation_bytes"]; !ok {
		t.Fatal("the billing copy lost the metering property")
	}
	if _, ok := billing["reviewer_note"]; ok {
		t.Fatal("the billing copy kept an audit property")
	}
	if _, ok := billing["device_id"]; ok {
		t.Fatal("the billing copy kept an identifier, which it refuses outright")
	}
}

// A value that must be findable but not readable is hashed at write time, and a
// value that must not be carried at all is removed.
func TestSplitAppliesSensitiveTreatment(t *testing.T) {
	s := splitter(t)
	copies, err := s.Split(context.Background(), issued(t), composed(t))
	if err != nil {
		t.Fatal(err)
	}
	security := byProfile(t, copies)["security"].GetData().GetFields()

	device := security["device_id"].GetStringValue()
	if device == "device-42" {
		t.Fatal("a property marked for hashing was written in clear")
	}
	if !keys.IsPseudonym(device) {
		t.Fatalf("device_id = %q, want a hashed value", device)
	}
	if _, ok := security["internal_note"]; ok {
		t.Fatal("a property marked for redaction was carried")
	}
}

// A property the schema does not describe cannot be judged, and what cannot be
// judged is not kept.
func TestSplitDropsUndescribedProperties(t *testing.T) {
	s := splitter(t)
	r := issued(t)
	r.Data.Fields["smuggled"] = structpb.NewStringValue("not in the schema")
	copies, err := s.Split(context.Background(), r, composed(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range copies {
		if _, ok := c.GetData().GetFields()["smuggled"]; ok {
			t.Fatalf("the %s copy kept a property no schema describes", c.GetProfile())
		}
	}
}

// The bags carry no annotations, so they go where the audit class goes.
func TestSplitCarriesTheBagsOnlyWhereAuditGoes(t *testing.T) {
	s := splitter(t)
	copies, err := s.Split(context.Background(), issued(t), composed(t))
	if err != nil {
		t.Fatal(err)
	}
	got := byProfile(t, copies)
	if len(got["security"].GetAttributes()) == 0 {
		t.Fatal("the security copy lost the attributes")
	}
	if len(got["billing"].GetAttributes()) != 0 {
		t.Fatal("the billing copy kept the attributes")
	}
}

// A profile nobody configured is not an error, but an operator should be told
// once rather than never.
func TestUnhandledProfilesAreReported(t *testing.T) {
	s := splitter(t)
	unhandled := s.Unhandled(composed(t))
	if len(unhandled) != 1 || unhandled[0] != "nowhere" {
		t.Fatalf("unhandled = %v, want the one profile this deployment lacks", unhandled)
	}
}

// Writing an identifier in clear because no key provider was configured is the
// one failure that must never be quiet.
func TestSplitRefusesToPseudonymiseWithNoKeys(t *testing.T) {
	s := &writer.Splitter{Profiles: profiles(t)}
	_, err := s.Split(context.Background(), issued(t), composed(t))
	if err == nil || !strings.Contains(err.Error(), "no key provider") {
		t.Fatalf("want a refusal naming the missing keys, got %v", err)
	}
}

// Every core field must be nameable by a preset, or a preset that names one
// keeps nothing and nobody notices.
func TestCoreFieldsCoverTheRecord(t *testing.T) {
	fields := map[string]bool{}
	for _, f := range writer.CoreFields() {
		fields[f] = true
	}
	for _, want := range []string{
		"/id", "/occurred_at", "/recorded_at", "/schema_version", "/catalogue_version",
		"/source", "/observer", "/sequence", "/action", "/operation", "/outcome",
		"/tenant_id", "/subject", "/actor", "/targets", "/context", "/capture",
		"/previous_attributes", "/data", "/meter", "/attributes", "/unmapped", "/origin_hash",
	} {
		if !fields[want] {
			t.Errorf("%s cannot be named by a preset", want)
		}
	}
}
