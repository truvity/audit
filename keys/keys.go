// Package keys holds the two kinds of key this system needs and the interfaces
// a deployment plugs its own behind.
//
// A Provider gives the symmetric keys that turn an identifier into a keyed
// pseudonym. There is one key per tenant and per purpose, which is what stops
// two copies of the same event being joined on a person: the security copy and
// the billing copy of one sign-in carry different pseudonyms for the same
// subject, and nothing short of both keys relates them.
//
// A Signer gives the asymmetric key the digest chain is signed with.
//
// Keys are never rotated. Rotation would break linkability across time for one
// person, which is the property the security copy is kept for. They are
// destroyed instead, and destroying one is how a subject's records become
// unlinkable: the pseudonyms remain, and nothing can recompute them.
package keys

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Purpose is what a key is used for. A profile's name is the usual purpose, so
// that each copy of a record pseudonymises under a key of its own.
type Purpose string

// PseudonymPrefix marks a value as a keyed pseudonym rather than an identifier
// anything else would recognise. A reader who does not know that costs an
// investigation its first hour.
const PseudonymPrefix = "ps_"

// Provider gives keyed pseudonyms and destroys the keys behind them.
type Provider interface {
	// Pseudonym returns the pseudonym of an identifier under a tenant's key for
	// a purpose, creating the key if there is none.
	Pseudonym(ctx context.Context, tenant string, purpose Purpose, identifier string) (string, error)
	// Destroy removes a key. Pseudonyms computed under it stay in the archive
	// and can never be recomputed, which is what erasure means here.
	Destroy(ctx context.Context, tenant string, purpose Purpose) error
	// Close releases whatever the provider holds.
	Close() error
}

// ErrDestroyed is returned when a key has been destroyed. Asking for a
// pseudonym under it is a fault in the caller, not a reason to mint a new key:
// minting one would give a person a second, unrelated identity in the same
// trail.
var ErrDestroyed = errors.New("keys: the key has been destroyed")

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}$`)

// pseudonym is the one place the derivation lives, so that two implementations
// of Provider cannot disagree about what a pseudonym is.
func pseudonym(key []byte, identifier string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(identifier))
	return PseudonymPrefix + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// IsPseudonym reports whether a value was produced by this package.
func IsPseudonym(v string) bool { return strings.HasPrefix(v, PseudonymPrefix) }

// checkName holds tenant and purpose to what may appear in a key's name, since
// both end up in paths and policies.
func checkName(tenant string, purpose Purpose) error {
	if !safeName.MatchString(tenant) {
		return fmt.Errorf("keys: tenant %q is not a usable name", tenant)
	}
	if !safeName.MatchString(string(purpose)) {
		return fmt.Errorf("keys: purpose %q is not a usable name", purpose)
	}
	return nil
}
