package catalogue

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"
)

// schemaWithExpiry adds a valid_until property to the wallet schema, marked as
// the credential's expiry with the given extra annotations.
func schemaWithExpiry(annotation string) string {
	return strings.Replace(walletSchema, `"properties": {
    "credential_format": {`, `"properties": {
    "valid_until": {
      "type": "string",
      "x-audit-class": "evidence",
      "x-audit-pii": "none",
      `+annotation+`
    },
    "credential_format": {`, 1)
}

// An expiry is a point in time spelled one way, so the mark is refused on
// anything that is not an RFC 3339 date-time string.
func TestTheExpiryMarkIsOnlyForADateTime(t *testing.T) {
	if err := loadWith(t, walletDoc, schemaWithExpiry(`"format": "date-time", "x-audit-expiry": true`)); err != nil {
		t.Fatalf("a date-time expiry was refused: %v", err)
	}
	if err := loadWith(t, walletDoc, schemaWithExpiry(`"x-audit-expiry": true`)); err == nil {
		t.Fatal("an expiry without format date-time was accepted")
	}
	integer := strings.Replace(schemaWithExpiry(`"format": "date-time", "x-audit-expiry": true`),
		`"valid_until": {
      "type": "string"`, `"valid_until": {
      "type": "integer"`, 1)
	if err := loadWith(t, walletDoc, integer); err == nil {
		t.Fatal("an integer expiry was accepted")
	}
}

func TestExpiryReadsTheMarkedProperty(t *testing.T) {
	c, err := Load([]byte(walletDoc), [][]byte{[]byte(schemaWithExpiry(`"format": "date-time", "x-audit-expiry": true`))})
	if err != nil {
		t.Fatal(err)
	}
	x, err := c.Compose("wallet.credential.issued")
	if err != nil {
		t.Fatal(err)
	}
	r := issued(t)
	if got, err := x.Expiry(r); err != nil || got != nil {
		t.Fatalf("a record without the property: %v %v", got, err)
	}

	data, _ := structpb.NewStruct(map[string]any{"credential_format": "sd-jwt-vc", "valid_until": "2031-09-17T00:00:00+02:00"})
	r.Data = data
	got, err := x.Expiry(r)
	if err != nil || got == nil || got.Format("2006-01-02T15:04:05Z07:00") != "2031-09-16T22:00:00Z" {
		t.Fatalf("expiry %v %v", got, err)
	}

	bad, _ := structpb.NewStruct(map[string]any{"valid_until": "next spring"})
	r.Data = bad
	if _, err := x.Expiry(r); err == nil {
		t.Fatal("a marked value that is not a time was read without complaint")
	}
}

// renewal is the wallet catalogue with its issuance action declared as an
// addendum to the records its renews property names.
func renewal(extends string) string {
	return strings.Replace(walletDoc, "    data_version: \"1\"\n", "    data_version: \"1\"\n    extends: "+extends+"\n", 1)
}

// schemaWithRenews is the wallet schema with an expiry and a renews list.
func schemaWithRenews(expiry string) string {
	return strings.Replace(schemaWithExpiry(expiry), `"credential_format": {`, `"renews": {
      "type": "array",
      "items": {"type": "string", "x-audit-class": "evidence", "x-audit-pii": "none"},
      "x-audit-class": "evidence",
      "x-audit-pii": "none"
    },
    "credential_format": {`, 1)
}

// An addendum the writer cannot act on is refused when the catalogue loads,
// not discovered when the lock it should have lengthened runs out.
func TestExtendsIsHeldToWhatTheWriterNeeds(t *testing.T) {
	marked := `"format": "date-time", "x-audit-expiry": true`
	if err := loadWith(t, renewal("/renews"), schemaWithRenews(marked)); err != nil {
		t.Fatalf("a well-formed addendum was refused: %v", err)
	}
	for name, c := range map[string]struct{ doc, schema string }{
		"an undeclared property":     {renewal("/replaces"), schemaWithRenews(marked)},
		"a property of a wrong type": {renewal("/attestation_bytes"), schemaWithRenews(marked)},
		"no expiry to extend to":     {renewal("/renews"), schemaWithRenews(`"format": "date-time"`)},
	} {
		if err := loadWith(t, c.doc, c.schema); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestExtendsReadsTheNamedRecords(t *testing.T) {
	c, err := Load([]byte(renewal("/renews")),
		[][]byte{[]byte(schemaWithRenews(`"format": "date-time", "x-audit-expiry": true`))})
	if err != nil {
		t.Fatal(err)
	}
	x, err := c.Compose("wallet.credential.issued")
	if err != nil {
		t.Fatal(err)
	}
	r := issued(t)
	if got, err := x.Extends(r); err != nil || len(got) != 0 {
		t.Fatalf("a record naming nothing: %v %v", got, err)
	}
	data, _ := structpb.NewStruct(map[string]any{"renews": []any{"rec-1", "", "rec-2"}})
	r.Data = data
	got, err := x.Extends(r)
	if err != nil || strings.Join(got, ",") != "rec-1,rec-2" {
		t.Fatalf("extends %v %v", got, err)
	}
}
