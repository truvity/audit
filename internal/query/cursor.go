package query

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/record"
)

// cursor is what a page hands back so the next one can be asked for.
//
// It is opaque to the caller and is not a promise about the implementation: it
// carries the boundary the searcher gave, which way the caller was reading, and
// a hash of the query it belongs to.
//
// The hash is the point. A cursor replayed against a different query would
// resume from a position in an ordering that no longer exists — silently
// returning rows that neither page of the new query would contain, or skipping
// rows that both would. Binding it means the caller is told rather than
// misled.
type cursor struct {
	Values     []string  `json:"v,omitempty"`
	ID         string    `json:"i,omitempty"`
	RecordedAt time.Time `json:"r,omitempty"`
	Sequence   uint64    `json:"s,omitempty"`
	Backwards  bool      `json:"b,omitempty"`
	Query      string    `json:"q"`
}

// ErrCursorMismatch is returned for a cursor from another query.
var ErrCursorMismatch = errors.New("query: this cursor belongs to a different query")

// encodeCursor renders a boundary for the wire.
func encodeCursor(at *index.Boundary, backwards bool, fingerprint string) (string, error) {
	if at == nil {
		return "", nil
	}
	body, err := json.Marshal(cursor{
		Values: at.Values, ID: at.ID, RecordedAt: at.RecordedAt,
		Sequence: at.Sequence, Backwards: backwards, Query: fingerprint,
	})
	if err != nil {
		return "", fmt.Errorf("query: %w", err)
	}
	// Canonical before encoding, so that the same boundary is the same string:
	// a cursor that differed by key order would break any caller that compared
	// or cached one.
	canonical, err := record.CanonicalJSON(body)
	if err != nil {
		return "", fmt.Errorf("query: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(canonical), nil
}

// decodeCursor reads a cursor and refuses one that belongs elsewhere.
func decodeCursor(raw, fingerprint string) (*index.Boundary, bool, error) {
	if raw == "" {
		return nil, false, nil
	}
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, false, fmt.Errorf("%w: this cursor is not one of ours: %v", ErrMalformed, err)
	}
	var c cursor
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, false, fmt.Errorf("%w: this cursor is not one of ours: %v", ErrMalformed, err)
	}
	if c.Query != fingerprint {
		return nil, false, fmt.Errorf(
			"%w: ask the same question again, or start from the first page", ErrCursorMismatch)
	}
	if len(c.Values) == 0 && c.ID == "" {
		// The `first` cursor: this question, from the beginning. It is a real
		// cursor so a caller can hold one of a kind rather than two.
		return nil, false, nil
	}
	return &index.Boundary{
		Values: c.Values, ID: c.ID, RecordedAt: c.RecordedAt, Sequence: c.Sequence,
	}, c.Backwards, nil
}

// fingerprint identifies the question a cursor belongs to.
//
// It covers everything that decides which rows come back and in what order —
// including the grant's narrowing, so a cursor issued under one grant cannot be
// replayed under a wider one. It deliberately excludes the limit: asking for a
// different page size is the same question, and a caller should not lose its
// place for changing it.
func fingerprint(q index.Query) (string, error) {
	shape := struct {
		Profile string              `json:"profile"`
		Tenants []string            `json:"tenants,omitempty"`
		Filter  []index.Conjunction `json:"filter,omitempty"`
		Sort    []index.SortBy      `json:"sort,omitempty"`
	}{q.Profile, q.Tenants, q.Filter, q.Sort}

	body, err := json.Marshal(shape)
	if err != nil {
		return "", fmt.Errorf("query: %w", err)
	}
	canonical, err := record.CanonicalJSON(body)
	if err != nil {
		return "", fmt.Errorf("query: %w", err)
	}
	sum := sha256.Sum256(canonical)
	// Half the digest: this identifies a question, it does not protect one, and
	// a cursor a person might paste into a terminal is better short.
	return hex.EncodeToString(sum[:16]), nil
}
