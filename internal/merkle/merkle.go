// Package merkle is the Merkle tree of docs/reference/bucket-contract.md: the
// Merkle Tree Hash of RFC 6962 section 2.1, over SHA-256, and the audit path
// that proves one leaf is in it (RFC 9162 section 2.1.3 for the checking).
//
// A seal's root is this tree over the hash of every record of an hour. The
// leaf input is the 32 raw bytes of a record's hash; this package knows
// nothing of records and takes any input.
package merkle

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math/bits"
)

// Size is the length of a hash, in bytes.
const Size = sha256.Size

// Hash is a node of the tree.
type Hash [Size]byte

// Domain separation: a leaf and an interior node never hash alike, so a node
// cannot be passed off as a leaf.
const (
	leafPrefix = 0x00
	nodePrefix = 0x01
)

// LeafHash is the hash of one leaf: SHA-256(0x00 || input).
func LeafHash(input []byte) Hash {
	h := sha256.New()
	h.Write([]byte{leafPrefix})
	h.Write(input)
	var out Hash
	h.Sum(out[:0])
	return out
}

// NodeHash is the hash of an interior node: SHA-256(0x01 || left || right).
func NodeHash(left, right Hash) Hash {
	var buf [1 + 2*Size]byte
	buf[0] = nodePrefix
	copy(buf[1:], left[:])
	copy(buf[1+Size:], right[:])
	return sha256.Sum256(buf[:])
}

// Empty is the root of a tree with no leaves: the SHA-256 of the empty string.
func Empty() Hash { return sha256.Sum256(nil) }

// split is the largest power of two smaller than n, for n above one: where
// RFC 6962 divides n leaves, the left subtree taking that many.
func split(n int) int { return 1 << (bits.Len(uint(n-1)) - 1) }

// Root is the Merkle Tree Hash of leaves, which are leaf hashes (LeafHash of
// each input) in order. The tree is unbalanced when their number is not a power
// of two, and no leaf is duplicated.
func Root(leaves []Hash) Hash {
	switch len(leaves) {
	case 0:
		return Empty()
	case 1:
		return leaves[0]
	}
	k := split(len(leaves))
	return NodeHash(Root(leaves[:k]), Root(leaves[k:]))
}

// Proof is the audit path of the leaf at index among leaves: the sibling
// hashes, from the leaf up, that recompute the root with the leaf.
func Proof(leaves []Hash, index int) ([]Hash, error) {
	if index < 0 || index >= len(leaves) {
		return nil, fmt.Errorf("merkle: leaf %d is not one of %d", index, len(leaves))
	}
	return path(index, leaves), nil
}

func path(m int, leaves []Hash) []Hash {
	if len(leaves) == 1 {
		return nil
	}
	k := split(len(leaves))
	if m < k {
		return append(path(m, leaves[:k]), Root(leaves[k:]))
	}
	return append(path(m-k, leaves[k:]), Root(leaves[:k]))
}

// Verify checks that the leaf at index of a tree of size leaves, with this
// audit path, hashes to root. It is the algorithm of RFC 9162 section
// 2.1.3.2, which accepts only a path of exactly the length the tree's shape
// gives that leaf.
func Verify(leaf Hash, index, size int, proof []Hash, root Hash) error {
	if size <= 0 || index < 0 || index >= size {
		return fmt.Errorf("merkle: leaf %d is not one of %d", index, size)
	}
	fn, sn := index, size-1
	r := leaf
	for _, p := range proof {
		if sn == 0 {
			return errors.New("merkle: the path is longer than the tree is deep")
		}
		if fn&1 == 1 || fn == sn {
			r = NodeHash(p, r)
			if fn&1 == 0 {
				for fn&1 == 0 && fn != 0 {
					fn >>= 1
					sn >>= 1
				}
			}
		} else {
			r = NodeHash(r, p)
		}
		fn >>= 1
		sn >>= 1
	}
	if sn != 0 {
		return errors.New("merkle: the path is shorter than the tree is deep")
	}
	if r != root {
		return errors.New("merkle: the path does not lead to the root")
	}
	return nil
}
