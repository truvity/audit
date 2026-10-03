// Package ulid makes the identifiers that end a record object's key.
//
// A ULID is 128 bits: 48 of milliseconds since the epoch, then 80 of
// randomness, written as 26 characters of Crockford base32. Keys that differ
// only in their ULID therefore sort in the order the ULIDs were made, which is
// what the bucket contract asks of an ingest batch's key.
//
// It is here rather than imported because the contract needs a monotonic
// generator and a parser, and that is thirty lines.
package ulid

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// alphabet is Crockford's base32 without I, L, O and U, in value order, which
// is also byte order: the encoding of a larger number is the later string.
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Length is the number of characters in a ULID.
const Length = 26

// Generator makes ULIDs that never go backwards.
//
// Within one millisecond the randomness is incremented rather than redrawn, so
// two ULIDs made in the same millisecond still sort in the order they were
// made. A clock that steps back does not make the generator step back: the
// timestamp stays at the last one issued.
type Generator struct {
	mu   sync.Mutex
	ms   uint64
	rand [10]byte
	// Entropy is where randomness comes from; crypto/rand when nil.
	Entropy func([]byte) error
}

// New returns a ULID for the given moment, and the moment it encodes. The
// second value is what a key's hour must be taken from, so that the hour and
// the ULID agree even when the clock went backwards.
func (g *Generator) New(at time.Time) (string, time.Time, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ms := uint64(at.UnixMilli())
	switch {
	case ms > g.ms:
		g.ms = ms
		if err := g.fill(); err != nil {
			return "", time.Time{}, err
		}
	default:
		// The same millisecond, or an earlier one: carry on from the last.
		if !increment(&g.rand) {
			// 2^80 ULIDs in one millisecond is not a thing that happens, but a
			// wrap would reuse a key, so move to the next millisecond.
			g.ms++
			if err := g.fill(); err != nil {
				return "", time.Time{}, err
			}
		}
	}
	return encode(g.ms, g.rand), time.UnixMilli(int64(g.ms)).UTC(), nil
}

func (g *Generator) fill() error {
	read := g.Entropy
	if read == nil {
		read = func(b []byte) error { _, err := rand.Read(b); return err }
	}
	if err := read(g.rand[:]); err != nil {
		return fmt.Errorf("ulid: entropy: %w", err)
	}
	// Leave the top bit clear so that incrementing within a millisecond has
	// room to run before it could wrap.
	g.rand[0] &= 0x7f
	return nil
}

// increment adds one to the 80-bit number, reporting whether it fitted.
func increment(b *[10]byte) bool {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return true
		}
	}
	return false
}

func encode(ms uint64, entropy [10]byte) string {
	var raw [16]byte
	for i := 5; i >= 0; i-- {
		raw[i] = byte(ms)
		ms >>= 8
	}
	copy(raw[6:], entropy[:])

	var out [Length]byte
	// 128 bits are written as 130, with two leading zero bits, so that the
	// first character is at most 7 and the string sorts as the number does.
	acc, bits, n := uint64(0), 2, 0
	for _, by := range raw {
		acc = acc<<8 | uint64(by)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[n] = alphabet[(acc>>uint(bits))&31]
			n++
		}
	}
	return string(out[:])
}

// From makes the ULID of a moment and a serial: the serial fills the low bits
// of the randomness. It is for fixtures, where keys have to be predictable.
func From(at time.Time, serial uint64) string {
	var entropy [10]byte
	for i := 9; i >= 2 && serial > 0; i-- {
		entropy[i] = byte(serial)
		serial >>= 8
	}
	return encode(uint64(at.UnixMilli()), entropy)
}

// Valid reports whether s is a well-formed ULID: 26 characters of the
// alphabet, not above the largest ULID.
func Valid(s string) bool {
	if len(s) != Length || s[0] > '7' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(alphabet, rune(s[i])) {
			return false
		}
	}
	return true
}

// Time is the moment a ULID encodes.
func Time(s string) (time.Time, error) {
	if !Valid(s) {
		return time.Time{}, errors.New("ulid: not a ULID: " + s)
	}
	var ms uint64
	for i := 0; i < 10; i++ {
		ms = ms<<5 | uint64(strings.IndexByte(alphabet, s[i]))
	}
	return time.UnixMilli(int64(ms)).UTC(), nil
}
