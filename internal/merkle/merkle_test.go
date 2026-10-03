package merkle_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/truvity/audit/internal/merkle"
)

// input is the leaf input of the i-th record of a vector: the 32 raw bytes of
// a record's hash, which here is the SHA-256 of the number written in decimal.
func input(i int) []byte {
	sum := sha256.Sum256([]byte(strconv.Itoa(i)))
	return sum[:]
}

func leaves(n int) []merkle.Hash {
	out := make([]merkle.Hash, n)
	for i := range out {
		out[i] = merkle.LeafHash(input(i))
	}
	return out
}

// naive is RFC 6962 section 2.1 as it is written, over the inputs themselves,
// sharing nothing with the package: the recursion splits at the largest power
// of two smaller than n, found by doubling.
func naive(inputs [][]byte) [32]byte {
	switch len(inputs) {
	case 0:
		return sha256.Sum256(nil)
	case 1:
		return sha256.Sum256(append([]byte{0}, inputs[0]...))
	}
	k := 1
	for k*2 < len(inputs) {
		k *= 2
	}
	l, r := naive(inputs[:k]), naive(inputs[k:])
	return sha256.Sum256(append(append([]byte{1}, l[:]...), r[:]...))
}

type vector struct {
	N      int      `json:"n"`
	Inputs []string `json:"inputs"`
	Root   string   `json:"root"`
	// Proofs are the audit paths, leaf by leaf, as hex.
	Proofs [][]string `json:"proofs"`
}

// The vectors for n = 0, 1, 2, 3 and 5 are committed, so that a second
// implementation can be held to the same numbers. They are written once, from
// the recursion of the RFC, and are not regenerated: a change here is a change
// to the contract.
func TestVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []vector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 5 {
		t.Fatalf("%d vectors, want n = 0, 1, 2, 3 and 5", len(vectors))
	}
	for i, n := range []int{0, 1, 2, 3, 5} {
		v := vectors[i]
		if v.N != n || len(v.Inputs) != n || len(v.Proofs) != n {
			t.Fatalf("vector %d is for n=%d with %d inputs and %d proofs", i, v.N, len(v.Inputs), len(v.Proofs))
		}
		hashes := make([]merkle.Hash, n)
		raws := make([][]byte, n)
		for j, in := range v.Inputs {
			if raws[j], err = hex.DecodeString(in); err != nil || len(raws[j]) != 32 {
				t.Fatalf("n=%d input %d: %v", n, j, err)
			}
			if in != hex.EncodeToString(input(j)) {
				t.Fatalf("n=%d input %d is not the documented input", n, j)
			}
			hashes[j] = merkle.LeafHash(raws[j])
		}
		root := merkle.Root(hashes)
		if got := hex.EncodeToString(root[:]); got != v.Root {
			t.Errorf("n=%d: root %s, the vector says %s", n, got, v.Root)
		}
		if n == 0 && v.Root != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
			t.Errorf("the empty root is not the SHA-256 of the empty string: %s", v.Root)
		}
		ref := naive(raws)
		if hex.EncodeToString(ref[:]) != v.Root {
			t.Errorf("n=%d: RFC 6962 as written gives %x, the vector says %s", n, ref, v.Root)
		}
		for j := range hashes {
			proof, err := merkle.Proof(hashes, j)
			if err != nil {
				t.Fatal(err)
			}
			if len(proof) != len(v.Proofs[j]) {
				t.Fatalf("n=%d leaf %d: a path of %d, the vector has %d", n, j, len(proof), len(v.Proofs[j]))
			}
			for k, p := range proof {
				if hex.EncodeToString(p[:]) != v.Proofs[j][k] {
					t.Errorf("n=%d leaf %d: path element %d is %x, the vector has %s", n, j, k, p, v.Proofs[j][k])
				}
			}
		}
	}
}

// For every size to forty the package agrees with the recursion as the RFC
// writes it, and every leaf's path checks.
func TestAgainstTheRFCAndProofs(t *testing.T) {
	for n := 0; n <= 40; n++ {
		hashes := leaves(n)
		raws := make([][]byte, n)
		for i := range raws {
			raws[i] = input(i)
		}
		root := merkle.Root(hashes)
		if ref := naive(raws); root != ref {
			t.Fatalf("n=%d: %x, RFC 6962 gives %x", n, root, ref)
		}
		for i := 0; i < n; i++ {
			proof, err := merkle.Proof(hashes, i)
			if err != nil {
				t.Fatal(err)
			}
			if err := merkle.Verify(hashes[i], i, n, proof, root); err != nil {
				t.Fatalf("n=%d leaf %d: %v", n, i, err)
			}
		}
	}
}

func TestProofsRefuse(t *testing.T) {
	const n = 11
	hashes := leaves(n)
	root := merkle.Root(hashes)
	proof, _ := merkle.Proof(hashes, 6)

	if err := merkle.Verify(hashes[5], 6, n, proof, root); err == nil {
		t.Error("another leaf verified at this index")
	}
	if err := merkle.Verify(hashes[6], 5, n, proof, root); err == nil {
		t.Error("the path verified at another index")
	}
	if err := merkle.Verify(hashes[6], 6, 19, proof, root); err == nil {
		t.Error("the path verified in a deeper tree")
	}
	if err := merkle.Verify(hashes[6], 6, n, proof[1:], root); err == nil {
		t.Error("a short path verified")
	}
	if err := merkle.Verify(hashes[6], 6, n, append(append([]merkle.Hash{}, proof...), proof[0]), root); err == nil {
		t.Error("a long path verified")
	}
	bad := append([]merkle.Hash{}, proof...)
	bad[0][0] ^= 1
	if err := merkle.Verify(hashes[6], 6, n, bad, root); err == nil {
		t.Error("a changed path verified")
	}
	if _, err := merkle.Proof(hashes, n); err == nil {
		t.Error("a proof of a leaf that is not there")
	}
	if err := merkle.Verify(hashes[0], 0, 0, nil, merkle.Empty()); err == nil {
		t.Error("a leaf verified in an empty tree")
	}
	// A leaf is not an interior node: the domain separation holds.
	if merkle.LeafHash(append(hashes[0][:], hashes[1][:]...)) == merkle.NodeHash(hashes[0], hashes[1]) {
		t.Error("a leaf and a node hash alike")
	}
}
