package catalogue

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/record"
)

const walletSchema = `{
  "$id": "https://schemas.example/wallet/credential-issued.json",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "credential_format": {
      "type": "string",
      "enum": ["sd-jwt-vc", "mdoc"],
      "x-audit-class": "shared",
      "x-audit-pii": "none",
      "x-audit-facet": true,
      "x-ocsf-path": "unmapped.credential_format"
    },
    "credential_id": {
      "type": "string",
      "x-audit-class": "evidence",
      "x-audit-pii": "none",
      "x-audit-filter": true
    },
    "attestation_bytes": {
      "type": "integer",
      "x-audit-class": "metering",
      "x-audit-pii": "none"
    }
  }
}`

const walletDoc = `
source: wallet
version: "1.0.0"
locales: [en]
actor_kinds:
  holder:
    category: external
    description: A person using a wallet.
target_types:
  credential: { description: "An issued credential." }
meters:
  issued:
    kind: count
    unit: credential
    description: Credentials issued.
actions:
  wallet.credential.issued:
    summary: A credential was issued to a holder.
    operation: create
    categories: [credential_lifecycle]
    profiles: [security, evidence, billing]
    target_types: [credential]
    data_schema: https://schemas.example/wallet/credential-issued.json
    data_version: "1"
    message:
      en: "{actor} was issued a {data.credential_format} credential"
    meter:
      name: issued
`

func wallet(t *testing.T) *Catalogue {
	t.Helper()
	c, err := Load([]byte(walletDoc), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatalf("the example catalogue does not load: %v", err)
	}
	return c
}

func loadWith(t *testing.T, doc string, schemas ...string) error {
	t.Helper()
	raw := make([][]byte, 0, len(schemas))
	for _, s := range schemas {
		raw = append(raw, []byte(s))
	}
	_, err := Load([]byte(doc), raw)
	return err
}

// An action belongs to the source that emits it. Without this a catalogue could
// describe another component's events and the viewer would show them as though
// that component had reported them.
func TestLoadRefusesAnActionOutsideItsNamespace(t *testing.T) {
	doc := strings.Replace(walletDoc, "wallet.credential.issued:", "issuer.credential.issued:", 1)
	err := loadWith(t, doc, walletSchema)
	if err == nil || !strings.Contains(err.Error(), "not under the namespace") {
		t.Fatalf("want a namespace refusal, got %v", err)
	}
}

func TestLoadRefusesAnUndeclaredTargetType(t *testing.T) {
	doc := strings.Replace(walletDoc, "target_types: [credential]", "target_types: [passport]", 1)
	err := loadWith(t, doc, walletSchema)
	if err == nil || !strings.Contains(err.Error(), "passport") {
		t.Fatalf("want a refusal naming the undeclared type, got %v", err)
	}
}

func TestLoadRefusesAnUndeclaredMeter(t *testing.T) {
	doc := strings.Replace(walletDoc, "      name: issued", "      name: minted", 1)
	err := loadWith(t, doc, walletSchema)
	if err == nil || !strings.Contains(err.Error(), "minted") {
		t.Fatalf("want a refusal naming the undeclared meter, got %v", err)
	}
}

// A gauge is measured by absolute samples, so an action that samples one has to
// say where the sample is read from.
func TestLoadRefusesAGaugeWithNoQuantityPath(t *testing.T) {
	doc := strings.Replace(walletDoc, "    kind: count", "    kind: gauge", 1)
	err := loadWith(t, doc, walletSchema)
	if err == nil || !strings.Contains(err.Error(), "quantity path") {
		t.Fatalf("want a refusal about the missing quantity path, got %v", err)
	}
}

func TestLoadRefusesAnUnreferencedSchema(t *testing.T) {
	spare := strings.Replace(walletSchema, "credential-issued.json", "spare.json", 1)
	err := loadWith(t, walletDoc, walletSchema, spare)
	if err == nil || !strings.Contains(err.Error(), "nothing references it") {
		t.Fatalf("want a refusal for the unused schema, got %v", err)
	}
}

func TestLoadRefusesAMissingSchema(t *testing.T) {
	err := loadWith(t, walletDoc)
	if err == nil || !strings.Contains(err.Error(), "was not supplied") {
		t.Fatalf("want a refusal for the missing schema, got %v", err)
	}
}

// A template that names something no record carries renders as a gap in the one
// place a reader is entitled to a straight sentence.
func TestLoadRefusesATemplateNamingUnknownData(t *testing.T) {
	doc := strings.Replace(walletDoc, "{data.credential_format}", "{data.holder_name}", 1)
	err := loadWith(t, doc, walletSchema)
	if err == nil || !strings.Contains(err.Error(), "holder_name") {
		t.Fatalf("want a refusal naming the unknown argument, got %v", err)
	}
}

func TestLoadRefusesAMissingLocale(t *testing.T) {
	doc := strings.Replace(walletDoc, "locales: [en]", "locales: [en, nl]", 1)
	err := loadWith(t, doc, walletSchema)
	if err == nil || !strings.Contains(err.Error(), `locale "nl"`) {
		t.Fatalf("want a refusal for the missing locale, got %v", err)
	}
}

func TestLoadRefusesAnExtensionSchemaThatLeaks(t *testing.T) {
	leaky := strings.Replace(walletSchema, `"x-audit-pii": "none",
      "x-audit-facet": true`, `"x-audit-pii": "direct"`, 1)
	err := loadWith(t, walletDoc, leaky)
	if err == nil {
		t.Fatal("a property declared as a direct identity attribute must be refused")
	}
}

func TestSchemaAnnotations(t *testing.T) {
	c := wallet(t)
	s, ok := c.Schema("https://schemas.example/wallet/credential-issued.json")
	if !ok {
		t.Fatal("the schema was not indexed")
	}
	if got := s.Facets(); len(got) != 1 || got[0] != "/credential_format" {
		t.Fatalf("facets = %v, want [/credential_format]", got)
	}
	if got := s.Filterable(); len(got) != 2 {
		t.Fatalf("filterable = %v, want the facet and the filter property", got)
	}
	if p := s.Properties["/attestation_bytes"]; p.Class != "metering" {
		t.Fatalf("attestation_bytes class = %q, want metering", p.Class)
	}
	if p := s.Properties["/credential_format"]; p.OCSFPath == "" {
		t.Fatal("the export mapping annotation was not indexed")
	}
}

func issued(t *testing.T) *record.Record {
	t.Helper()
	data, err := structpb.NewStruct(map[string]any{
		"credential_format": "sd-jwt-vc",
		"credential_id":     "cred-1",
		"attestation_bytes": 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &record.Record{
		Id:               record.NewID(),
		OccurredAt:       timestamppb.Now(),
		SchemaVersion:    record.SchemaVersion,
		CatalogueVersion: "1.0.0",
		Source:           "wallet",
		Action:           "wallet.credential.issued",
		Operation:        auditv1.Operation_OPERATION_CREATE,
		Outcome:          &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
		TenantId:         "acme",
		Actor:            &record.Actor{Kind: "holder", Id: "holder-1"},
		Targets:          []*record.Target{{Type: "credential", Id: "cred-1"}},
		Data:             data,
		Meter: &record.Meter{
			Name: "issued", Quantity: "1", Unit: "credential", Kind: auditv1.Meter_KIND_COUNT,
		},
	}
}

func TestComposedValidateAcceptsAWellFormedRecord(t *testing.T) {
	x, err := wallet(t).Compose("wallet.credential.issued")
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Validate(issued(t)); err != nil {
		t.Fatalf("a well formed record was refused: %v", err)
	}
}

func TestComposedValidateRefusals(t *testing.T) {
	x, err := wallet(t).Compose("wallet.credential.issued")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*record.Record)
		want string
	}{
		{"wrong operation", func(r *record.Record) {
			r.Operation = auditv1.Operation_OPERATION_REMOVE
		}, "declared as create"},
		{"undeclared actor kind", func(r *record.Record) {
			r.Actor.Kind = "ghost"
		}, "not declared"},
		{"undeclared target type", func(r *record.Record) {
			r.Targets[0].Type = "passport"
		}, "not declared"},
		{"data the schema does not allow", func(r *record.Record) {
			r.Data.Fields["holder_name"] = structpb.NewStringValue("Alice")
		}, "does not satisfy"},
		{"a value outside the enum", func(r *record.Record) {
			r.Data.Fields["credential_format"] = structpb.NewStringValue("pdf")
		}, "does not satisfy"},
		{"no meter on a metered action", func(r *record.Record) {
			r.Meter = nil
		}, "must carry the measurement"},
		{"the wrong unit", func(r *record.Record) {
			r.Meter.Unit = "byte"
		}, "measured in"},
		{"a catalogue version that is not this one", func(r *record.Record) {
			r.CatalogueVersion = "9.9.9"
		}, "catalogue version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := issued(t)
			tc.edit(r)
			err := x.Validate(r)
			if err == nil {
				t.Fatal("want refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// A target type that is a person is named by identifier only: a display name is
// an identity attribute, and the record carries none.
func TestComposedValidateRefusesANamedPerson(t *testing.T) {
	doc := strings.Replace(walletDoc,
		`  credential: { description: "An issued credential." }`,
		`  credential: { description: "An issued credential." }
  holder: { description: "A person.", is_person: true }`, 1)
	doc = strings.Replace(doc, "target_types: [credential]", "target_types: [credential, holder]", 1)
	c, err := Load([]byte(doc), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	x, err := c.Compose("wallet.credential.issued")
	if err != nil {
		t.Fatal(err)
	}
	r := issued(t)
	r.Targets = append(r.Targets, &record.Target{Type: "holder", Id: "holder-1", Name: "Alice Smith"})
	err = x.Validate(r)
	if err == nil || !strings.Contains(err.Error(), "may carry no name") {
		t.Fatalf("want a refusal for the named person, got %v", err)
	}
}

func TestMessageArguments(t *testing.T) {
	for _, tc := range []struct {
		template string
		want     []string
	}{
		{"{actor} signed in", []string{"actor"}},
		{"{actor} removed {targets.0.id} from {targets.1.id}", []string{"actor", "targets.0.id", "targets.1.id"}},
		{"{count, plural, one {# record} other {# records}}", []string{"count"}},
		{"{a} and {b, select, x {{c}} other {}}", []string{"a", "b", "c"}},
		{"no arguments here", nil},
		{"'{not an argument}' but {this} is", []string{"this"}},
		{"{dup} and {dup}", []string{"dup"}},
	} {
		got := MessageArguments(tc.template)
		if len(got) != len(tc.want) {
			t.Errorf("MessageArguments(%q) = %v, want %v", tc.template, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("MessageArguments(%q) = %v, want %v", tc.template, got, tc.want)
				break
			}
		}
	}
}

// A profile may claim to satisfy a framework only if something in the
// installation actually records the events that framework asks for.
func TestMissingCategories(t *testing.T) {
	c := wallet(t)
	required := []string{"credential_lifecycle", "authentication"}
	missing := MissingCategories("security", required, []*Catalogue{c})
	if len(missing) != 1 || missing[0] != "authentication" {
		t.Fatalf("missing = %v, want [authentication]", missing)
	}
	if got := MissingCategories("billing", []string{"credential_lifecycle"}, []*Catalogue{c}); len(got) != 0 {
		t.Fatalf("missing = %v, want none", got)
	}
	// A profile nothing emits into covers nothing.
	if got := MissingCategories("history", required, []*Catalogue{c}); len(got) != 2 {
		t.Fatalf("missing = %v, want both categories", got)
	}
}
