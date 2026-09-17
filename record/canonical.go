package record

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// marshalOptions is the protobuf JSON mapping this project writes: proto field
// names (snake_case), enums as strings, unpopulated fields omitted. protojson
// deliberately varies its whitespace, so its output is never written or hashed
// directly — Canonical re-serialises it.
var marshalOptions = protojson.MarshalOptions{
	UseProtoNames:   true,
	UseEnumNumbers:  false,
	EmitUnpopulated: false,
}

// unmarshalOptions accepts both snake_case and lowerCamelCase on input, as the
// protobuf JSON mapping requires, and refuses fields the schema does not know
// rather than dropping them silently.
var unmarshalOptions = protojson.UnmarshalOptions{DiscardUnknown: false}

// Marshal returns the canonical JSON of a record: one NDJSON line, byte-stable
// for as long as the record is kept.
func Marshal(r *Record) ([]byte, error) { return Canonical(r) }

// Unmarshal decodes a record from its JSON form.
func Unmarshal(b []byte, r *Record) error { return unmarshalOptions.Unmarshal(b, r) }

// Canonical returns the record in canonical form: RFC 8785 applied to the
// protobuf JSON mapping. Every hash in this system is taken over these bytes.
func Canonical(m proto.Message) ([]byte, error) {
	raw, err := marshalOptions.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("audit: marshal: %w", err)
	}
	return canonicalJSON(raw)
}

// Size is the length of the canonical form, which is what bounds are measured
// against and what an object's byte count is made of.
func Size(r *Record) int {
	b, err := Canonical(r)
	if err != nil {
		return 0
	}
	return len(b)
}

// OriginHash is the commitment the writer makes to the record it accepted:
// SHA-256, lowercase hex, over the canonical form with profile and origin_hash
// unset.
//
// Every profile copy of one record carries the same value, so copies can be
// joined and shown to descend from one original. It is not recomputable from a
// copy, because a copy has had fields removed by design; proving that a copy
// itself is unaltered is the digest chain's job, not this field's.
func OriginHash(r *Record) (string, error) {
	c := proto.Clone(r).(*Record)
	c.Profile = ""
	c.OriginHash = ""
	b, err := Canonical(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// CanonicalJSON re-serialises arbitrary JSON per RFC 8785. It is exported
// because a digest is signed over its own canonical form, and signing and
// checking must not be able to disagree about whitespace or key order.
func CanonicalJSON(raw []byte) ([]byte, error) { return canonicalJSON(raw) }

// canonicalJSON re-serialises arbitrary JSON per RFC 8785.
func canonicalJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("audit: canonicalise: %w", err)
	}
	var b strings.Builder
	if err := writeCanonical(&b, v); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func writeCanonical(b *strings.Builder, v any) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeCanonicalString(b, t)
	case json.Number:
		n, err := canonicalNumber(t)
		if err != nil {
			return err
		}
		b.WriteString(n)
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeCanonical(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonicalString(b, k)
			b.WriteByte(':')
			if err := writeCanonical(b, t[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("audit: canonicalise: unexpected %T", v)
	}
	return nil
}

// canonicalNumber formats a JSON number. Only integers that a float64 holds
// exactly are accepted: an audit record carries counts and sizes as integers
// and decimal quantities as strings, so no floating-point formatter is needed
// and no rounding can ever change a stored byte.
func canonicalNumber(n json.Number) (string, error) {
	s := n.String()
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		if i > 1<<53-1 || i < -(1<<53-1) {
			return "", fmt.Errorf("audit: number %s is outside the exactly representable range; carry it as a string", s)
		}
		return strconv.FormatInt(i, 10), nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("audit: number %s: %w", s, err)
	}
	if math.IsInf(f, 0) || math.IsNaN(f) || f != math.Trunc(f) {
		return "", fmt.Errorf("audit: number %s is not an integer; an audit record carries decimals as strings", s)
	}
	if math.Abs(f) > 1<<53-1 {
		return "", fmt.Errorf("audit: number %s is outside the exactly representable range; carry it as a string", s)
	}
	return strconv.FormatInt(int64(f), 10), nil
}

// writeCanonicalString applies the escaping of RFC 8785 section 3.2.2.2: the
// two mandatory escapes, the five short forms, \u00xx for the remaining control
// characters, and every other code point literally.
func writeCanonicalString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
				continue
			}
			if r == utf8.RuneError {
				b.WriteString("�")
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// lessUTF16 orders strings by UTF-16 code unit, which is what RFC 8785
// specifies. For the ASCII keys this format allows it is byte order; the full
// comparison is implemented so a key outside ASCII could never sort two ways.
func lessUTF16(a, b string) bool {
	au, bu := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(au) && i < len(bu); i++ {
		if au[i] != bu[i] {
			return au[i] < bu[i]
		}
	}
	return len(au) < len(bu)
}
