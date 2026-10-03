package store

import (
	"context"
	"sort"
	"strings"
	"time"
)

// The seals and keys half of the v1 layout, as docs/reference/bucket-contract.md
// specifies it. Like the records half in layout.go, built and read here and
// nowhere else.

const (
	// SealsPrefix is the prefix under which every seal lives.
	SealsPrefix = "seals/"
	// KeysPrefix is the prefix of the trust anchors, the delegations and the
	// revocations.
	KeysPrefix = "keys/"
	// RootsKey is the JWK Set of root public keys: distribution, not trust.
	RootsKey = KeysPrefix + "roots.jwks"
	// DelegationsPrefix holds keys/delegations/<thumbprint>/<ULID>.jws.
	DelegationsPrefix = KeysPrefix + "delegations/"
	// RevocationsPrefix holds keys/revocations/<ULID>.jws.
	RevocationsPrefix = KeysPrefix + "revocations/"
)

// SealExt is the extension of a seal's key.
const SealExt = ".jws"

// SealTenantPrefix is where one profile's and tenant's seals live.
func SealTenantPrefix(profile, tenant string) string {
	return SealsPrefix + profile + "/" + tenant + "/"
}

// SealKey is the key of the seal of one hour:
// seals/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>.jws.
func SealKey(profile, tenant string, hour time.Time) string {
	return SealTenantPrefix(profile, tenant) + hour.UTC().Format("2006/01/02/15") + SealExt
}

// SealObject is a seal's key, taken apart.
type SealObject struct {
	Profile, Tenant string
	Hour            time.Time
}

// ParseSealKey reads a key of the grammar
//
//	seals/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>.jws
//
// and reports whether it is one.
func ParseSealKey(key string) (SealObject, bool) {
	rest, ok := strings.CutPrefix(key, SealsPrefix)
	if !ok {
		return SealObject{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 6 || parts[0] == "" || parts[1] == "" {
		return SealObject{}, false
	}
	last, ok := strings.CutSuffix(parts[5], SealExt)
	if !ok {
		return SealObject{}, false
	}
	var n [4]int
	for i, field := range []struct {
		s     string
		width int
	}{{parts[2], 4}, {parts[3], 2}, {parts[4], 2}, {last, 2}} {
		if len(field.s) != field.width {
			return SealObject{}, false
		}
		for _, c := range field.s {
			if c < '0' || c > '9' {
				return SealObject{}, false
			}
			n[i] = n[i]*10 + int(c-'0')
		}
	}
	o := SealObject{Profile: parts[0], Tenant: parts[1],
		Hour: time.Date(n[0], time.Month(n[1]), n[2], n[3], 0, 0, 0, time.UTC)}
	// A date that does not exist is normalised by time.Date.
	if SealKey(o.Profile, o.Tenant, o.Hour) != key {
		return SealObject{}, false
	}
	return o, true
}

// WalkSeals visits the seals of one profile and tenant for the hours from
// `from` to `to`, both inclusive, in key order, which is time order.
func WalkSeals(
	ctx context.Context, s Store, profile, tenant string, from, to time.Time, fn func(Entry) error,
) error {
	first := from.UTC().Truncate(time.Hour)
	last := to.UTC().Truncate(time.Hour)
	if last.Before(first) {
		return nil
	}
	prefix := SealTenantPrefix(profile, tenant)
	// Every key of the first hour sorts after the key just before it, and
	// nothing at or past the key of the hour after the last is in range.
	start := SealKey(profile, tenant, first.Add(-time.Hour))
	stop := SealKey(profile, tenant, last.Add(time.Hour))
	for {
		entries, err := s.List(ctx, prefix, start, pageSize)
		if err != nil {
			return err
		}
		for _, e := range entries {
			start = e.Key
			if e.Key >= stop {
				return nil
			}
			if err := fn(e); err != nil {
				return err
			}
		}
		if len(entries) < pageSize {
			return nil
		}
	}
}

// LastSeal is the newest seal of one profile and tenant, or false when there
// is none. It descends year, month, day by common prefix and lists one day,
// and so costs five requests however many seals the tenant has; a key under the
// prefix that is not a seal's is passed over.
func LastSeal(ctx context.Context, s Store, profile, tenant string) (Entry, bool, error) {
	prefix := SealTenantPrefix(profile, tenant)
	// Each level is tried from the newest down, so that a stray prefix that
	// sorts last (or a day of nothing but strays) costs a step and not the
	// answer.
	var descend func(prefix string, depth int) (Entry, bool, error)
	descend = func(prefix string, depth int) (Entry, bool, error) {
		if depth == 3 {
			entries, err := s.List(ctx, prefix, "", 0)
			if err != nil {
				return Entry{}, false, err
			}
			for i := len(entries) - 1; i >= 0; i-- {
				if _, ok := ParseSealKey(entries[i].Key); ok {
					return entries[i], true, nil
				}
			}
			return Entry{}, false, nil
		}
		groups, err := s.Prefixes(ctx, prefix, "/")
		if err != nil {
			return Entry{}, false, err
		}
		sort.Strings(groups)
		for i := len(groups) - 1; i >= 0; i-- {
			if e, ok, err := descend(groups[i], depth+1); err != nil || ok {
				return e, ok, err
			}
		}
		return Entry{}, false, nil
	}
	return descend(prefix, 0)
}
