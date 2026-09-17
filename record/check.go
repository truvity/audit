package record

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
)

var (
	sourcePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)
	actionPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)+$`)
	kindPattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	decimal       = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
	jwtPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*$`)
)

// forbiddenWords are the names a key in a free-form map may not be made of. A
// key is split on _ . - : and both each segment and each adjacent pair joined
// are looked up, so "refresh_token" and "api_key" are refused while
// "tokenisation" and "key_id" are not.
//
// The list names secret material and the few identifiers that never belong in
// a trail, and nothing else. It deliberately does not contain "credential",
// "key" or "auth": those are the working vocabulary of an identity product,
// and a list that refuses credential_format teaches emitters to route around
// the check. What catches an actual secret carried under an innocent name is
// forbiddenValue, which reads the value itself.
//
// The way past a refusal is not a longer list of exceptions: it is to declare
// the property in the action's data schema, where a value that must be findable
// but not readable is annotated x-audit-sensitive and is hashed or redacted at
// write time.
var forbiddenWords = map[string]bool{
	"password": true, "passwd": true, "pwd": true, "passphrase": true,
	"secret": true, "token": true, "authorization": true,
	"apikey": true, "privatekey": true, "secretkey": true, "accesskey": true,
	"cookie": true, "pan": true, "cvv": true, "cvc": true,
	"iban": true, "ssn": true, "bsn": true,
}

// Check refuses a record that must not be written: one that is incomplete, one
// whose names are malformed, and one that carries what an audit trail may never
// carry. Every problem found is reported, not just the first, because an
// emitter is usually fixed once for all of them.
func Check(r *Record, b Bounds) error {
	if r == nil {
		return errors.New("audit: nil record")
	}
	var problems []error
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(format, args...))
	}

	if r.GetId() == "" {
		fail("id is required")
	} else if _, err := uuid.Parse(r.GetId()); err != nil {
		fail("id %q is not a UUID", r.GetId())
	}
	if r.GetOccurredAt() == nil {
		fail("occurred_at is required")
	}
	if r.GetSchemaVersion() == "" {
		fail("schema_version is required")
	} else if _, _, err := ParseSchemaVersion(r.GetSchemaVersion()); err != nil {
		fail("%v", err)
	}
	if r.GetCatalogueVersion() == "" {
		fail("catalogue_version is required: a record must name the catalogue that describes it")
	}
	switch {
	case r.GetSource() == "":
		fail("source is required")
	case !sourcePattern.MatchString(r.GetSource()):
		fail("source %q must match %s", r.GetSource(), sourcePattern)
	}
	switch {
	case r.GetAction() == "":
		fail("action is required")
	case !actionPattern.MatchString(r.GetAction()):
		fail("action %q must be resource.verb under the source namespace", r.GetAction())
	case r.GetSource() != "" && !strings.HasPrefix(r.GetAction(), r.GetSource()+"."):
		fail("action %q is not under the namespace of source %q", r.GetAction(), r.GetSource())
	}
	if r.GetOperation() == auditv1.Operation_OPERATION_UNSPECIFIED {
		fail("operation is required")
	}
	if r.GetOutcome().GetResult() == auditv1.Outcome_RESULT_UNSPECIFIED {
		fail("outcome.result is required")
	}
	switch {
	case r.GetTenantId() == "":
		fail("tenant_id is required; use %s for a record that belongs to the installation", TenantPlatform)
	case strings.HasPrefix(r.GetTenantId(), "@") && r.GetTenantId() != TenantPlatform:
		fail("tenant_id %q is reserved: only %s may begin with @", r.GetTenantId(), TenantPlatform)
	}

	if a := r.GetActor(); a != nil {
		if !kindPattern.MatchString(a.GetKind()) {
			fail("actor.kind %q must match %s", a.GetKind(), kindPattern)
		}
		if a.GetId() == "" && a.GetKind() != "system" {
			fail("actor.id is required for actor kind %q", a.GetKind())
		}
	}
	if p := r.GetSubject(); p != nil {
		if !kindPattern.MatchString(p.GetKind()) {
			fail("subject.kind %q must match %s", p.GetKind(), kindPattern)
		}
		if p.GetId() == "" {
			fail("subject.id is required when a subject is named")
		}
	}
	for i, t := range r.GetTargets() {
		if !kindPattern.MatchString(t.GetType()) {
			fail("targets[%d].type %q must match %s", i, t.GetType(), kindPattern)
		}
		if t.GetId() == "" {
			fail("targets[%d].id is required", i)
		}
	}
	if m := r.GetMeter(); m != nil {
		if m.GetName() == "" {
			fail("meter.name is required")
		}
		if m.GetKind() == auditv1.Meter_KIND_UNSPECIFIED {
			fail("meter.kind is required: a count or a gauge sample")
		}
		if m.GetUnit() == "" {
			fail("meter.unit is required")
		}
		if !decimal.MatchString(m.GetQuantity()) {
			fail("meter.quantity %q must be an exact decimal string", m.GetQuantity())
		}
	}

	problems = append(problems, checkSecrets(r)...)
	problems = append(problems, checkBounds(r, b)...)
	return errors.Join(problems...)
}

// checkSecrets applies the negative list to everything free-form.
func checkSecrets(r *Record) []error {
	var problems []error
	for k, v := range r.GetAttributes() {
		problems = append(problems, scanPair("attributes", k, v)...)
	}
	problems = append(problems, scanStruct("data", r.GetData())...)
	problems = append(problems, scanStruct("unmapped", r.GetUnmapped())...)
	if c := r.GetCapture(); c != nil {
		problems = append(problems, scanStruct("capture.request", c.GetRequest())...)
		problems = append(problems, scanStruct("capture.response", c.GetResponse())...)
	}
	if a := r.GetActor(); a != nil {
		problems = append(problems, scanStruct("actor.attributes", a.GetAttributes())...)
	}
	for i, t := range r.GetTargets() {
		problems = append(problems, scanStruct(fmt.Sprintf("targets[%d].attributes", i), t.GetAttributes())...)
	}
	if c := r.GetContext(); c != nil {
		for area, s := range c.GetAreas() {
			problems = append(problems, scanStruct("context.areas."+area, s)...)
		}
	}
	if m := r.GetMeter(); m != nil {
		problems = append(problems, scanStruct("meter.dimensions", m.GetDimensions())...)
	}
	return problems
}

func scanStruct(path string, s *structpb.Struct) []error {
	if s == nil {
		return nil
	}
	var problems []error
	for k, v := range s.GetFields() {
		problems = append(problems, scanValue(path+"."+k, k, v)...)
	}
	return problems
}

func scanValue(path, key string, v *structpb.Value) []error {
	var problems []error
	if key != "" {
		if word := forbiddenKey(key); word != "" {
			problems = append(problems, fmt.Errorf("%s: %s", path, whyForbidden(word)))
		}
	}
	switch t := v.GetKind().(type) {
	case *structpb.Value_StringValue:
		if why := forbiddenValue(t.StringValue); why != "" {
			problems = append(problems, fmt.Errorf("%s: %s", path, why))
		}
	case *structpb.Value_StructValue:
		for k, inner := range t.StructValue.GetFields() {
			problems = append(problems, scanValue(path+"."+k, k, inner)...)
		}
	case *structpb.Value_ListValue:
		for i, inner := range t.ListValue.GetValues() {
			problems = append(problems, scanValue(fmt.Sprintf("%s[%d]", path, i), "", inner)...)
		}
	}
	return problems
}

func scanPair(path, key, value string) []error {
	var problems []error
	if word := forbiddenKey(key); word != "" {
		problems = append(problems, fmt.Errorf("%s.%s: %s", path, key, whyForbidden(word)))
	}
	if why := forbiddenValue(value); why != "" {
		problems = append(problems, fmt.Errorf("%s.%s: %s", path, key, why))
	}
	return problems
}

// whyForbidden says what is wrong and, as importantly, what to do instead.
func whyForbidden(word string) string {
	return fmt.Sprintf("a key named for %q may not be written; declare the property in the "+
		"action's data schema with x-audit-sensitive if it must be carried", word)
}

// forbiddenKey reports the name that makes a key inadmissible, or "".
func forbiddenKey(key string) string {
	segments := strings.FieldsFunc(strings.ToLower(key), func(r rune) bool {
		return r == '_' || r == '.' || r == '-' || r == ':' || r == ' '
	})
	for i, seg := range segments {
		if forbiddenWords[seg] {
			return seg
		}
		if i+1 < len(segments) {
			if joined := seg + segments[i+1]; forbiddenWords[joined] {
				return joined
			}
		}
	}
	return ""
}

// forbiddenValue reports why a value may not be written, or "". The three
// shapes it recognises — a PEM block, a JSON Web Token, a bearer credential —
// are never something an audit record needs and always something it must not
// keep.
func forbiddenValue(v string) string {
	if strings.Contains(v, "-----BEGIN ") {
		return "the value carries a PEM block"
	}
	if len(v) >= 8 && strings.EqualFold(v[:7], "bearer ") {
		return "the value carries a bearer credential"
	}
	if looksLikeJWT(v) {
		return "the value is a JSON Web Token; carry its hash or its claims, never the token"
	}
	return ""
}

func looksLikeJWT(v string) bool {
	if len(v) < 32 || !jwtPattern.MatchString(v) {
		return false
	}
	head, _, _ := strings.Cut(v, ".")
	raw, err := base64.RawURLEncoding.DecodeString(head)
	if err != nil {
		return false
	}
	s := strings.ReplaceAll(string(raw), " ", "")
	return strings.HasPrefix(s, `{"alg"`) || strings.HasPrefix(s, `{"typ"`) ||
		strings.Contains(s, `"alg":`) && strings.HasPrefix(s, "{")
}

func checkBounds(r *Record, b Bounds) []error {
	var problems []error
	if b.MaxBytes > 0 {
		if n := Size(r); n > b.MaxBytes {
			problems = append(problems, fmt.Errorf("record is %d bytes, over the %d byte bound", n, b.MaxBytes))
		}
	}
	if b.AttributeKeys > 0 && len(r.GetAttributes()) > b.AttributeKeys {
		problems = append(problems, fmt.Errorf("attributes has %d keys, over the %d bound", len(r.GetAttributes()), b.AttributeKeys))
	}
	if b.Targets > 0 && len(r.GetTargets()) > b.Targets {
		problems = append(problems, fmt.Errorf("targets has %d entries, over the %d bound", len(r.GetTargets()), b.Targets))
	}
	return problems
}
