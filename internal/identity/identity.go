// Package identity is the way back from a pseudonym to the person, for the
// cases the law requires and only for them.
//
// A pseudonym is a keyed hash, and a hash does not run backwards. So when the
// writer pseudonymises an actor or a subject, it also keeps the identifier,
// sealed under the same tenant's key for the same purpose, in the archive:
// identity/tenant=<t>/purpose=<p>/<pseudonym>. Whoever resolves must hold that
// key; nobody else can open the entry. And destroying the key — erasure —
// makes every sealed identifier under it unreadable with it, so crypto-shredding
// still means what it says.
//
// The entries live in the archive rather than the index for durability's sake:
// an entry the writer failed to keep can never be recreated, since the plain
// identifier exists nowhere else, so it is written with the same guarantees as
// the records and does not depend on the database being up.
//
// Only identities are kept: actors and subjects. Extension values marked
// x-audit-sensitive: hmac are hashed to be findable and never readable, and
// no way back is kept for them.
package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/store"
)

// Prefix is where sealed identities live, apart from the records.
const Prefix = "identity"

// ErrUnknown is a pseudonym the map holds no identity for: not one this
// deployment minted, or minted before identities were kept.
var ErrUnknown = errors.New("identity: no identity is kept for this pseudonym")

// Map keeps and resolves identities.
type Map struct {
	Store store.Store
	Keys  keys.Sealer
	// RetainUntil is how long an entry is locked. It wants to be the longest
	// any record carrying the pseudonym is kept.
	RetainUntil func(time.Time) time.Time
	Now         func() time.Time

	// kept remembers what this process has already written, so that a
	// pseudonym seen a thousand times costs one write.
	kept sync.Map
}

// Key is where one pseudonym's identity is kept.
func Key(tenant string, purpose keys.Purpose, pseudonym string) string {
	return fmt.Sprintf("%s/tenant=%s/purpose=%s/%s", Prefix, tenant, purpose, pseudonym)
}

// Remember keeps the identity behind a pseudonym. Keeping it twice is fine:
// the entry is written once and a second writer finds it there.
func (m *Map) Remember(ctx context.Context, tenant string, purpose keys.Purpose, pseudonym, identifier string) error {
	if pseudonym == "" || identifier == "" {
		return nil
	}
	key := Key(tenant, purpose, pseudonym)
	if _, done := m.kept.Load(key); done {
		return nil
	}
	sealed, err := m.Keys.Seal(ctx, tenant, purpose, []byte(identifier))
	if err != nil {
		return fmt.Errorf("identity: sealing %s: %w", key, err)
	}
	err = m.Store.Put(ctx, store.Object{
		Key: key, Body: sealed, RetainUntil: m.retainUntil(), ContentType: "application/octet-stream",
	})
	if err != nil && !errors.Is(err, store.ErrExists) {
		return fmt.Errorf("identity: keeping %s: %w", key, err)
	}
	m.kept.Store(key, true)
	return nil
}

// Resolve returns the identity behind a pseudonym, ErrUnknown when none is
// kept, or keys.ErrDestroyed when the tenant's key for the purpose is gone.
func (m *Map) Resolve(ctx context.Context, tenant string, purpose keys.Purpose, pseudonym string) (string, error) {
	if strings.ContainsAny(pseudonym, "/") || pseudonym == "" {
		return "", fmt.Errorf("%w: %q is not a pseudonym", ErrUnknown, pseudonym)
	}
	sealed, err := m.Store.Get(ctx, Key(tenant, purpose, pseudonym))
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrUnknown
	}
	if err != nil {
		return "", err
	}
	plain, err := m.Keys.Open(ctx, tenant, purpose, sealed)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (m *Map) retainUntil() time.Time {
	now := time.Now().UTC()
	if m.Now != nil {
		now = m.Now().UTC()
	}
	if m.RetainUntil != nil {
		return m.RetainUntil(now)
	}
	return now.AddDate(10, 0, 0)
}
