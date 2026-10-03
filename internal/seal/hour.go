package seal

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/truvity/audit/internal/merkle"
	"github.com/truvity/audit/internal/recobj"
	"github.com/truvity/audit/store"
)

// Hour is what the archive holds for one profile and tenant in one hour of
// ingest time, as a seal states it.
type Hour struct {
	// Objects, Count and Bytes are the number of record objects, of records in
	// them, and of their stored bytes.
	Objects int
	Count   int64
	Bytes   int64
	// Root is the Merkle root over the hash of every record: object by object
	// in key order, line by line in each.
	Root merkle.Hash
	// First and Last are the first and last object key of the hour.
	First, Last string
	// Retain is the latest retention of any object of the hour: a seal is locked
	// as long as the records it covers.
	Retain time.Time
	// Leaves are the leaf hashes, kept so that a record can be proven.
	Leaves []merkle.Hash
	// Problems are what is wrong with the objects, when they were read
	// strictly. A notary seals nothing that has any.
	Problems []string
}

// RootHex is the root as the seal writes it.
func (h *Hour) RootHex() string { return hex.EncodeToString(h.Root[:]) }

// Compute reads the record objects of one profile, tenant and hour and builds
// what a seal says of them.
//
// Strict is the notary's reading: each object's metadata must say what the
// object is (its sha256, its count, its format) and each record's hash must be
// the hash of its record, and anything else is a Problem. A notary that sealed
// an object whose bytes do not match its own metadata would be vouching for
// what it had not checked. The verifier reads leniently: it takes the hashes the
// lines state and compares the root with the seal's, because the object-level
// findings are the object check's to make and a single changed object should
// not hide as a bad root.
func Compute(
	ctx context.Context, s store.Store, profile, tenant string, hour time.Time, strict bool,
) (*Hour, error) {
	hour = hour.UTC().Truncate(time.Hour)
	h := &Hour{}
	err := store.WalkHours(ctx, s, profile, tenant, hour, hour, "", func(e store.Entry) error {
		parsed, ok := store.ParseRecordKey(e.Key)
		if !ok || parsed.Profile != profile || parsed.Tenant != tenant || !parsed.Hour.Equal(hour) {
			h.Problems = append(h.Problems, e.Key+": not a record object key of this profile, tenant and hour")
			return nil
		}
		head, err := s.Head(ctx, e.Key)
		if err != nil {
			return fmt.Errorf("seal: %s: %w", e.Key, err)
		}
		body, err := s.Get(ctx, e.Key)
		if err != nil {
			return fmt.Errorf("seal: %s: %w", e.Key, err)
		}
		lines, err := recobj.Decode(body)
		if err != nil {
			h.Problems = append(h.Problems, e.Key+": "+err.Error())
			return nil
		}
		if strict {
			for _, p := range recobj.CheckMetadata(head.Metadata, body, len(lines)) {
				h.Problems = append(h.Problems, e.Key+": "+p)
			}
		}
		for n, line := range lines {
			if strict {
				if err := line.Verify(); err != nil {
					h.Problems = append(h.Problems, fmt.Sprintf("%s: line %d: %v", e.Key, n+1, err))
					continue
				}
			}
			raw, err := hex.DecodeString(line.Hash)
			if err != nil || len(raw) != merkle.Size {
				h.Problems = append(h.Problems, fmt.Sprintf("%s: line %d: the hash is not 32 bytes of hex", e.Key, n+1))
				continue
			}
			h.Leaves = append(h.Leaves, merkle.LeafHash(raw))
		}
		if h.First == "" {
			h.First = e.Key
		}
		h.Last = e.Key
		h.Objects++
		h.Count += int64(len(lines))
		h.Bytes += e.Size
		if head.RetainUntil.After(h.Retain) {
			h.Retain = head.RetainUntil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	h.Root = merkle.Root(h.Leaves)
	return h, nil
}
