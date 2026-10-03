package seal

import (
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/truvity/audit/sdk/gen/audit/v1"
	"github.com/truvity/audit/sdk/record"
)

// MaxDelegation is the longest window a delegation may open, in seconds.
const MaxDelegation = 25 * 60 * 60

// The wire messages, audit.v1 in proto/audit/v1/seal.proto.
type (
	// Seal is the statement of one hour.
	Seal = auditv1.Seal
	// Delegation lets a key sign seals for a scope for a short while.
	Delegation = auditv1.Delegation
	// Revocation ends the trust in a key from a time.
	Revocation = auditv1.Revocation
)

var (
	// The proto field names, as the contract says, and every field written: a
	// count of zero is "0" and an empty `prev` is "", not absent, so that a
	// reader that does not know the proto defaults still reads what the seal
	// says.
	marshalOptions   = protojson.MarshalOptions{UseProtoNames: true, EmitDefaultValues: true}
	unmarshalOptions = protojson.UnmarshalOptions{DiscardUnknown: true}
)

// Marshal is a statement's payload: the proto JSON mapping with the proto field
// names, in canonical form (RFC 8785), so that the same statement is the same
// bytes however it was built.
func Marshal(m proto.Message) ([]byte, error) {
	raw, err := marshalOptions.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("seal: marshal: %w", err)
	}
	return record.CanonicalJSON(raw)
}

// Unmarshal reads a payload. A member this build does not know is ignored: a
// later minor version may add one, and the signature covers it either way.
func Unmarshal(payload []byte, m proto.Message) error {
	if err := unmarshalOptions.Unmarshal(payload, m); err != nil {
		return fmt.Errorf("seal: the payload does not parse: %w", err)
	}
	return nil
}

// PayloadError is a statement whose JWS is well-formed and whose payload is
// not what its type says: not a seal, or a seal that does not say what a seal
// must.
type PayloadError struct{ Reason string }

func (e *PayloadError) Error() string { return e.Reason }

func payloadError(format string, args ...any) error {
	return &PayloadError{Reason: fmt.Sprintf(format, args...)}
}

// Typed is a seal read from its bytes: the parsed token and the statement.
type Typed struct {
	Token *Token
	Seal  *Seal
}

// ParseSeal reads a seal and checks what a reader can check without a key: the
// shape of the JWS, the type, and that the payload is a seal that names its
// hour, profile, tenant and root.
func ParseSeal(stored []byte) (*Typed, error) {
	t, err := Parse(stored)
	if err != nil {
		return nil, err
	}
	if t.Typ != TypSeal {
		return nil, fmt.Errorf("seal: the type is %q, a seal's is %q", t.Typ, TypSeal)
	}
	s := &Seal{}
	if err := Unmarshal(t.Payload, s); err != nil {
		return nil, &PayloadError{Reason: err.Error()}
	}
	switch {
	case s.GetTenant() == "" || s.GetProfile() == "":
		return nil, payloadError("seal: a seal names its profile and its tenant")
	case s.GetHour() == nil || !s.GetHour().AsTime().Equal(s.GetHour().AsTime().Truncate(time.Hour)):
		return nil, payloadError("seal: the hour is not the start of an hour")
	case len(s.GetRoot()) != 64 || strings.ToLower(s.GetRoot()) != s.GetRoot():
		return nil, payloadError("seal: the root is not 64 characters of lower-case hex")
	case s.GetCount() < 0:
		return nil, payloadError("seal: the count is negative")
	case s.GetSealedAt() == nil:
		return nil, payloadError("seal: a seal says when it was made")
	}
	return &Typed{Token: t, Seal: s}, nil
}

// NewSeal builds the statement of an hour. The times are whole seconds.
func NewSeal(profile, tenant string, hour time.Time, h *Hour, prev string, at time.Time) *Seal {
	s := &Seal{
		Tenant: tenant, Profile: profile,
		Hour:     timestamppb.New(hour.UTC().Truncate(time.Hour)),
		Count:    h.Count,
		Root:     h.RootHex(),
		First:    h.First,
		Last:     h.Last,
		Prev:     prev,
		SealedAt: timestamppb.New(at.UTC().Truncate(time.Second)),
	}
	if h.Objects > 0 {
		s.Meters = map[string]int64{"objects": int64(h.Objects), "records": h.Count, "bytes": h.Bytes}
	}
	return s
}

// ParseDelegation reads a delegation's payload.
func ParseDelegation(payload []byte) (*Delegation, error) {
	d := &Delegation{}
	if err := Unmarshal(payload, d); err != nil {
		return nil, err
	}
	return d, nil
}

// ParseRevocation reads a revocation's payload.
func ParseRevocation(payload []byte) (*Revocation, error) {
	r := &Revocation{}
	if err := Unmarshal(payload, r); err != nil {
		return nil, err
	}
	return r, nil
}

// NewDelegation builds the statement of a delegation: key, window and scope.
// It is for a root's own tooling and for tests; the notary never signs one.
func NewDelegation(root string, sub JWK, nbf, exp time.Time, profiles, tenants []string) (*Delegation, error) {
	raw, err := marshalOptionsJWK(sub)
	if err != nil {
		return nil, err
	}
	return &Delegation{
		Iss: root, Sub: sub.Kid, Jwk: raw, Nbf: nbf.Unix(), Exp: exp.Unix(),
		Scope: &auditv1.DelegationScope{Profiles: profiles, Tenants: tenants},
	}, nil
}

func marshalOptionsJWK(j JWK) (*structpb.Struct, error) {
	return structpb.NewStruct(map[string]any{
		"kty": j.Kty, "crv": j.Crv, "x": j.X, "y": j.Y, "kid": j.Kid, "alg": j.Alg, "use": j.Use,
	})
}
