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
