// Package record is the canonical audit record: the generated protobuf type
// plus the rules the type alone cannot express — identifiers, bounds, the
// negative list, and the canonical JSON form that every hash is taken over.
//
// Applications do not normally import this package; they import emit, which
// calls into it. The split writer, the verifier and any third-party reader do
// import it, because the canonical form is part of the archive contract: an
// object written today must hash the same way when its retention ends years
// from now.
//
// # Canonical form
//
// The canonical form is JSON Canonicalization Scheme (RFC 8785) applied to the
// protobuf JSON mapping with proto field names and unpopulated fields omitted.
// Two restrictions make it reproducible without a floating-point formatter:
//
//   - Numbers in extension slots must be integers in the range where a float64
//     represents them exactly. Decimal quantities are strings (Meter.Quantity);
//     floating point has no place in a record that must be byte-identical for
//     years.
//   - Map keys are ASCII, so canonical key order is both byte order and UTF-16
//     code-unit order. The comparison is implemented on code units anyway.
package record
