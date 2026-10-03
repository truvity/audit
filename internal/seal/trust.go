package seal

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/truvity/audit/store"
)

// Skew is how far the bucket's clock may be from a seal's and still agree
// about when it was written. A verifier takes the bucket's time of writing as
// a second witness to `sealed_at`, which a key signs for itself.
const Skew = 5 * time.Minute

// Trust is what a verifier believes about keys: the roots it pins, and the
// delegations and revocations those roots signed.
//
// Only a pinned root is trusted. keys/roots.jwks, and everything else under
// keys/, is how the keys are distributed: it names a key and says nothing of
// whether to believe it, so a statement signed by a root that is not pinned is
// ignored as if it were not there, and writing to the bucket cannot add one.
type Trust struct {
	pins        map[string]bool
	roots       map[string]*ecdsa.PublicKey
	delegations map[string][]delegation
	revocations map[string][]time.Time
	// Ignored says, for each statement under keys/ that a pinned root's name is
	// on and that was not believed, why: a delegation of more than 25 hours, a
	// signature that does not verify. These are faults of the bucket.
	Ignored []Ignored
	// Foreign counts the statements that were not believed because no pinned
	// root signed them, or because they are not statements at all. They are
	// only in the bucket, and a verifier says no more of them than that.
	Foreign int
}

// Ignored is a statement under keys/ that a verifier does not believe, and why.
type Ignored struct{ Key, Reason string }

type delegation struct {
	key        *ecdsa.PublicKey
	nbf, exp   time.Time
	profiles   []string
	tenants    []string
	objectName string
}

// LoadTrust reads keys/ and keeps what the pinned roots vouch for. A bucket
// with no keys/roots.jwks is not an error here: the seals in it simply cannot
// be placed, and Check says so.
func LoadTrust(ctx context.Context, s store.Store, pins []string) (*Trust, error) {
	t := &Trust{
		pins: map[string]bool{}, roots: map[string]*ecdsa.PublicKey{},
		delegations: map[string][]delegation{}, revocations: map[string][]time.Time{},
	}
	for _, p := range pins {
		t.pins[p] = true
	}
	if len(t.pins) == 0 {
		return nil, errors.New("seal: a verifier pins at least one root: without one nothing a seal says can be believed")
	}

	body, err := s.Get(ctx, store.RootsKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Not a fault of its own: a seal whose key it should have held says so.
	case err != nil:
		return nil, err
	default:
		set, err := ParseJWKS(body)
		if err != nil {
			t.Ignored = append(t.Ignored, Ignored{store.RootsKey, err.Error()})
		}
		for thumbprint, pub := range set {
			if t.pins[thumbprint] {
				t.roots[thumbprint] = pub
			}
		}
	}

	delegations, err := s.List(ctx, store.DelegationsPrefix, "", 0)
	if err != nil {
		return nil, err
	}
	for _, e := range delegations {
		body, err := s.Get(ctx, e.Key)
		if err != nil {
			return nil, err
		}
		if err := t.addDelegation(e.Key, body); err != nil {
			t.note(e.Key, err)
		}
	}
	revocations, err := s.List(ctx, store.RevocationsPrefix, "", 0)
	if err != nil {
		return nil, err
	}
	for _, e := range revocations {
		body, err := s.Get(ctx, e.Key)
		if err != nil {
			return nil, err
		}
		if err := t.addRevocation(body); err != nil {
			t.note(e.Key, err)
		}
	}
	return t, nil
}

// signedByARoot checks a statement's header and signature against the pinned
// root it names.
func (t *Trust) signedByARoot(raw []byte, typ string) (*Token, error) {
	tok, err := Parse(raw)
	if err != nil {
		return nil, errForeign
	}
	if !t.pins[tok.Kid] {
		return nil, errForeign
	}
	if tok.Typ != typ {
		return nil, fmt.Errorf("the type is %q and should be %q", tok.Typ, typ)
	}
	pub, ok := t.roots[tok.Kid]
	if !ok {
		return nil, fmt.Errorf("signed by the pinned root %s, whose key is not in %s", tok.Kid, store.RootsKey)
	}
	if err := tok.Verify(pub); err != nil {
		return nil, err
	}
	return tok, nil
}

// errForeign marks a statement no pinned root named.
var errForeign = errors.New("not signed by a pinned root")

func (t *Trust) note(key string, err error) {
	if errors.Is(err, errForeign) {
		t.Foreign++
		return
	}
	t.Ignored = append(t.Ignored, Ignored{key, err.Error()})
}

func (t *Trust) addDelegation(name string, raw []byte) error {
	tok, err := t.signedByARoot(raw, TypDelegation)
	if err != nil {
		return err
	}
	d, err := ParseDelegation(tok.Payload)
	if err != nil {
		return err
	}
	if d.GetIss() != tok.Kid {
		return fmt.Errorf("it names %s as its issuer and is signed by %s", d.GetIss(), tok.Kid)
	}
	window := d.GetExp() - d.GetNbf()
	switch {
	case window <= 0:
		return errors.New("its window is empty")
	case window > MaxDelegation:
		return fmt.Errorf("its window is %s and a delegation is valid for at most 25 hours", time.Duration(window)*time.Second)
	case d.GetScope() == nil || len(d.GetScope().GetProfiles()) == 0 || len(d.GetScope().GetTenants()) == 0:
		return errors.New("it names no scope: profiles and tenants are lists of names or \"*\"")
	case d.GetJwk() == nil:
		return errors.New("it carries no key")
	}
	jwk := JWK{}
	for field, into := range map[string]*string{
		"kty": &jwk.Kty, "crv": &jwk.Crv, "x": &jwk.X, "y": &jwk.Y,
	} {
		if v, ok := d.GetJwk().GetFields()[field]; ok {
			*into = v.GetStringValue()
		}
	}
	pub, err := jwk.Key()
	if err != nil {
		return err
	}
	if got := Thumbprint(pub); got != d.GetSub() {
		return fmt.Errorf("it names %s as the delegated key and the key it carries is %s", d.GetSub(), got)
	}
	t.delegations[d.GetSub()] = append(t.delegations[d.GetSub()], delegation{
		key: pub, nbf: time.Unix(d.GetNbf(), 0).UTC(), exp: time.Unix(d.GetExp(), 0).UTC(),
		profiles: d.GetScope().GetProfiles(), tenants: d.GetScope().GetTenants(), objectName: name,
	})
	return nil
}

func (t *Trust) addRevocation(raw []byte) error {
	tok, err := t.signedByARoot(raw, TypRevocation)
	if err != nil {
		return err
	}
	r, err := ParseRevocation(tok.Payload)
	if err != nil {
		return err
	}
	if r.GetIss() != tok.Kid {
		return fmt.Errorf("it names %s as its issuer and is signed by %s", r.GetIss(), tok.Kid)
	}
	if r.GetRevokes() == "" || r.GetRevokedAt() == nil {
		return errors.New("it names no key or no time")
	}
	t.revocations[r.GetRevokes()] = append(t.revocations[r.GetRevokes()], r.GetRevokedAt().AsTime())
	return nil
}

func covers(list []string, name string) bool {
	for _, n := range list {
		if n == "*" || n == name {
			return true
		}
	}
	return false
}

// Check decides whether a seal is signed by a key to be believed: a pinned
// root, or a key a pinned root delegated to for this profile and tenant, with
// `sealed_at` in the window, and not revoked as of the seal. writtenAt is when
// the bucket says the seal was put, zero when it is not known: a key signs its
// own `sealed_at`, so a revoked or expired key could date a seal as it
// pleased, and the bucket's clock is the second witness.
func (t *Trust) Check(seal *Typed, writtenAt time.Time) error {
	kid := seal.Token.Kid
	if t.pins[kid] {
		pub, ok := t.roots[kid]
		if !ok {
			return fmt.Errorf("signed by the pinned root %s, whose key is not in %s", kid, store.RootsKey)
		}
		return seal.Token.Verify(pub)
	}

	candidates := t.delegations[kid]
	if len(candidates) == 0 {
		why := fmt.Sprintf("signed by %s, which is not a root this verifier pins and has no delegation from one", kid)
		if n := len(t.Ignored) + t.Foreign; n > 0 {
			why += fmt.Sprintf(" (%d statements under keys/ were not believed)", n)
		}
		return errors.New(why)
	}
	at := seal.Seal.GetSealedAt().AsTime()
	reasons := make([]string, 0, len(candidates))
	for _, d := range candidates {
		switch {
		case at.Before(d.nbf) || at.After(d.exp):
			reasons = append(reasons, fmt.Sprintf("sealed_at %s is outside the window %s to %s",
				at.Format(time.RFC3339), d.nbf.Format(time.RFC3339), d.exp.Format(time.RFC3339)))
		case !writtenAt.IsZero() && writtenAt.After(d.exp.Add(Skew)):
			reasons = append(reasons, fmt.Sprintf("the seal was written at %s, after the delegation ended at %s",
				writtenAt.Format(time.RFC3339), d.exp.Format(time.RFC3339)))
		case !covers(d.profiles, seal.Seal.GetProfile()) || !covers(d.tenants, seal.Seal.GetTenant()):
			reasons = append(reasons, fmt.Sprintf("profile %s, tenant %s is outside the delegation's scope",
				seal.Seal.GetProfile(), seal.Seal.GetTenant()))
		default:
			if err := seal.Token.Verify(d.key); err != nil {
				reasons = append(reasons, err.Error())
				continue
			}
			return t.notRevoked(kid, at, writtenAt)
		}
	}
	sort.Strings(reasons)
	return fmt.Errorf("signed by the delegated key %s, and no delegation vouches for this seal: %s", kid, reasons[0])
}

func (t *Trust) notRevoked(kid string, at, writtenAt time.Time) error {
	for _, revokedAt := range t.revocations[kid] {
		if !at.Before(revokedAt) || (!writtenAt.IsZero() && !writtenAt.Add(-Skew).Before(revokedAt)) {
			return fmt.Errorf("the key %s was revoked from %s", kid, revokedAt.Format(time.RFC3339))
		}
	}
	return nil
}
