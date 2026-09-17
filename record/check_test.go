package record

import (
	"encoding/base64"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
)

func TestCheckAcceptsAWellFormedRecord(t *testing.T) {
	r := sample(t)
	Normalise(r, Default)
	if err := Check(r, Default); err != nil {
		t.Fatalf("a well formed record was refused: %v", err)
	}
}

func TestCheckRequiresTheCoreFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Record)
		want string
	}{
		{"no id", func(r *Record) { r.Id = "" }, "id is required"},
		{"id not a uuid", func(r *Record) { r.Id = "not-a-uuid" }, "not a UUID"},
		{"no occurred_at", func(r *Record) { r.OccurredAt = nil }, "occurred_at is required"},
		{"no catalogue version", func(r *Record) { r.CatalogueVersion = "" }, "catalogue_version is required"},
		{"no source", func(r *Record) { r.Source = "" }, "source is required"},
		{"no action", func(r *Record) { r.Action = "" }, "action is required"},
		{"no operation", func(r *Record) { r.Operation = auditv1.Operation_OPERATION_UNSPECIFIED }, "operation is required"},
		{"no outcome", func(r *Record) { r.Outcome = nil }, "outcome.result is required"},
		{"no tenant", func(r *Record) { r.TenantId = "" }, "tenant_id is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := sample(t)
			tc.edit(r)
			err := Check(r, Default)
			if err == nil {
				t.Fatal("want refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// An action belongs to the source that emits it. Without this a reporter could
// write a record that reads as though another component produced it.
func TestCheckKeepsActionsInTheirSourceNamespace(t *testing.T) {
	r := sample(t)
	r.Action = "issuer.sign-in"
	err := Check(r, Default)
	if err == nil || !strings.Contains(err.Error(), "not under the namespace") {
		t.Fatalf("want a namespace refusal, got %v", err)
	}
}

func TestCheckReservesThePlatformTenant(t *testing.T) {
	r := sample(t)
	r.TenantId = "@someone-else"
	err := Check(r, Default)
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("want a reserved-tenant refusal, got %v", err)
	}

	ok := sample(t)
	ok.TenantId = TenantPlatform
	if err := Check(ok, Default); err != nil {
		t.Fatalf("the platform tenant must be accepted: %v", err)
	}
}

// The negative list is what keeps a secret out of an object that cannot be
// deleted for years.
func TestCheckRefusesSecretsByKey(t *testing.T) {
	for _, key := range []string{
		"password", "client_secret", "api_key", "authorization",
		"refresh_token", "private_key", "session.cookie", "card-pan",
	} {
		t.Run(key, func(t *testing.T) {
			r := sample(t)
			r.Attributes = map[string]string{key: "value"}
			err := Check(r, Default)
			if err == nil {
				t.Fatal("want refusal")
			}
			if !strings.Contains(err.Error(), "x-audit-sensitive") {
				t.Fatalf("the error should name the way out: %v", err)
			}
		})
	}
}

func TestCheckAllowsOrdinaryKeys(t *testing.T) {
	r := sample(t)
	r.Attributes = map[string]string{
		"how": "recovery", "proof": "workload", "tokenisation": "none", "keyboard": "qwerty",
	}
	if err := Check(r, Default); err != nil {
		t.Fatalf("ordinary keys were refused: %v", err)
	}
}

func TestCheckRefusesSecretsByValue(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	jwt := header + ".eyJzdWIiOiJhbGljZSJ9.c2lnbmF0dXJlLWJ5dGVzLWhlcmU"

	for _, tc := range []struct{ name, value, want string }{
		{"a JSON Web Token", jwt, "JSON Web Token"},
		{"a bearer credential", "Bearer abcdef0123456789", "bearer credential"},
		{"a PEM block", "-----BEGIN PRIVATE KEY-----\nMIIE", "PEM block"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := sample(t)
			r.Attributes = map[string]string{"detail": tc.value}
			err := Check(r, Default)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

func TestCheckLooksInsideExtensionSlots(t *testing.T) {
	nested, err := structpb.NewStruct(map[string]any{
		"outer": map[string]any{"inner": map[string]any{"client_secret": "shh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := sample(t)
	r.Data = nested
	err = Check(r, Default)
	if err == nil || !strings.Contains(err.Error(), "data.outer.inner.client_secret") {
		t.Fatalf("want the path of the offending property, got %v", err)
	}
}

func TestCheckReportsEveryProblemAtOnce(t *testing.T) {
	r := sample(t)
	r.Id = ""
	r.Source = ""
	r.TenantId = ""
	err := Check(r, Default)
	if err == nil {
		t.Fatal("want refusal")
	}
	for _, want := range []string{"id is required", "source is required", "tenant_id is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestCheckMeter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		meter *Meter
		want  string
	}{
		{"no name", &Meter{Quantity: "1", Unit: "call", Kind: auditv1.Meter_KIND_COUNT}, "meter.name is required"},
		{"no kind", &Meter{Name: "calls", Quantity: "1", Unit: "call"}, "meter.kind is required"},
		{"no unit", &Meter{Name: "calls", Quantity: "1", Kind: auditv1.Meter_KIND_COUNT}, "meter.unit is required"},
		{"float quantity", &Meter{Name: "calls", Quantity: "1e3", Unit: "call", Kind: auditv1.Meter_KIND_COUNT}, "exact decimal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := sample(t)
			r.Meter = tc.meter
			err := Check(r, Default)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}

	r := sample(t)
	r.Meter = &Meter{Name: "storage", Quantity: "1048576.25", Unit: "byte", Kind: auditv1.Meter_KIND_GAUGE}
	if err := Check(r, Default); err != nil {
		t.Fatalf("an exact decimal quantity was refused: %v", err)
	}
}

func TestReadable(t *testing.T) {
	for _, tc := range []struct {
		version string
		ok      bool
	}{
		{"1.0", true},
		{"0.9", false},
		{"2.0", false},
		{"1.7", false}, // a newer minor may carry fields this reader would drop
		{"nonsense", false},
	} {
		err := Readable(tc.version)
		if tc.ok != (err == nil) {
			t.Errorf("Readable(%q) = %v, want ok=%v", tc.version, err, tc.ok)
		}
	}
}
