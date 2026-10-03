package bucketcontract

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/truvity/audit/internal/seal"
	"github.com/truvity/audit/store"
)

// The rules of the seals half of the contract, as findings name them.
const (
	// RuleSealKey: a key under seals/ is seals/<profile>/<tenant>/<yyyy>/<mm>/
	// <dd>/<hh>.jws.
	RuleSealKey = "seal.key"
	// RuleSealJWS: a seal is a compact JWS, ES384, of type audit-seal+jws, that
	// names its key.
	RuleSealJWS = "seal.jws"
	// RuleSealPayload: the payload is an audit.v1.Seal that names the profile,
	// tenant and hour the key names, and says no sooner than the hour's end
	// when it was made.
	RuleSealPayload = "seal.payload"
	// RuleSealSignature: the signature verifies under a key to be believed: a
	// pinned root, or a key a pinned root delegated to within its window and
	// scope and that was not revoked.
	RuleSealSignature = "seal.signature"
	// RuleSealChain: prev is the SHA-256 of the previous seal's bytes, and the
	// first seal of a profile and tenant has none.
	RuleSealChain = "seal.chain"
	// RuleSealRoot: the count, the first and last key and the Merkle root are
	// those of the objects the hour holds.
	RuleSealRoot = "seal.root"
	// RuleSealMissing: an hour that has closed and settled has a seal, empty
	// hours too, from the hour of the tenant's first object on.
	RuleSealMissing = "seal.missing"
	// RuleSealLock: a seal is locked as long as the records it covers.
	RuleSealLock = "seal.lock"
	// RuleKeysRoots: keys/roots.jwks is a JWK Set of P-384 keys.
	RuleKeysRoots = "keys.roots"
	// RuleKeysStatement: a delegation or a revocation is a JWS of its type
	// signed by a pinned root; a delegation's window is at most 25 hours.
	// Statements that are not are ignored by a verifier, and said here.
	RuleKeysStatement = "keys.statement"
)

// SealOptions say what to check of the seals and with what to trust them.
type SealOptions struct {
	// Profile whose seals to check.
	Profile string
	// Roots are the thumbprints of the root keys the checker pins. Nothing in
	// the bucket is trusted but through them.
	Roots []string
	// From and To bound the hours checked, both inclusive.
	From, To time.Time
	// Now, Settle and Grace say which hours are due a seal: an hour is due
	// once it has ended and Settle and then Grace have passed.
	Now           time.Time
	Settle, Grace time.Duration
	// Lock is the Object Lock mode the profile demands of what it keeps; empty
	// or "none" checks no lock.
	Lock string
}

// SealChecked is a seal that was read and is without fault.
type SealChecked struct {
	Key  string
	Hour time.Time
	Head store.Entry
}

// SealReport is what a check of the seals found.
type SealReport struct {
	// Seals is how many were read.
	Seals int
	// Windows are the hours checked, as the prefix of the hour.
	Hours map[time.Time]bool
	// Clean are the seals that have nothing wrong.
	Clean []SealChecked
	// Findings are the rules broken. Hour is set on each.
	Findings []Finding
}

// Has reports whether any finding is of the rule.
func (r *SealReport) Has(rule string) bool {
	for _, f := range r.Findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

func (r *SealReport) String() string {
	var b strings.Builder
	for _, f := range r.Findings {
		b.WriteString(f.String() + "\n")
	}
	fmt.Fprintf(&b, "%d seals, %d findings", r.Seals, len(r.Findings))
	return b.String()
}

// CheckSeals holds the seals of a profile to the contract: for every tenant
// that has records or seals, and every hour of the range, whether there is a
// seal when there should be, whether it is signed by a key to be believed,
// whether it chains to the seal before it, and whether it says what the hour
// holds now.
//
// The last is what makes it worth having: a seal compared with the objects as
// they are in the bucket today, so that an object added, removed or changed
// after the seal was made breaks the root.
func CheckSeals(ctx context.Context, s store.Store, o SealOptions) (*SealReport, error) {
	if len(o.Roots) == 0 {
		return nil, errors.New("seals: pin at least one root: without one nothing a seal says can be believed")
	}
	trust, err := seal.LoadTrust(ctx, s, o.Roots)
	if err != nil {
		return nil, err
	}
	report := &SealReport{Hours: map[time.Time]bool{}}

	// The keys under keys/: what a verifier ignores, said once.
	for _, ignored := range trust.Ignored {
		rule := RuleKeysStatement
		if ignored.Key == store.RootsKey {
			rule = RuleKeysRoots
		}
		report.Findings = append(report.Findings, Finding{Rule: rule, Key: ignored.Key, Detail: ignored.Reason})
	}

	tenants := map[string]bool{}
	for _, prefix := range []string{store.ProfilePrefix(o.Profile), store.SealsPrefix + o.Profile + "/"} {
		found, err := s.Prefixes(ctx, prefix, "/")
		if err != nil {
			return nil, err
		}
		for _, p := range found {
			tenants[strings.TrimSuffix(strings.TrimPrefix(p, prefix), "/")] = true
		}
	}
	names := make([]string, 0, len(tenants))
	for t := range tenants {
		names = append(names, t)
	}
	sort.Strings(names)

	c := &sealChecker{s: s, o: o, trust: trust, report: report}
	c.from = o.From.UTC().Truncate(time.Hour)
	c.last = o.To.UTC().Truncate(time.Hour)
	// The last hour whose seal is due: it has ended, and the settle window and
	// the grace for the notary's schedule have passed.
	c.due = o.Now.UTC().Add(-o.Settle - o.Grace).Truncate(time.Hour).Add(-time.Hour)
	for _, tenant := range names {
		if err := c.tenant(ctx, tenant); err != nil {
			return nil, err
		}
	}
	return report, nil
}

type sealChecker struct {
	s               store.Store
	o               SealOptions
	trust           *seal.Trust
	report          *SealReport
	from, last, due time.Time
}

func (c *sealChecker) bad(hour time.Time, key, rule, format string, args ...any) {
	c.report.Hours[hour] = true
	c.report.Findings = append(c.report.Findings, Finding{
		Rule: rule, Key: key, Hour: hour, Detail: fmt.Sprintf(format, args...),
	})
}

func (c *sealChecker) tenant(ctx context.Context, tenant string) error {
	// The hour a tenant's seals begin at: that of its first object.
	var first time.Time
	if objects, err := c.s.List(ctx, store.TenantPrefix(c.o.Profile, tenant), "", 1); err != nil {
		return err
	} else if len(objects) > 0 {
		if o, ok := store.ParseRecordKey(objects[0].Key); ok {
			first = o.Hour
		}
	}

	have := map[time.Time]store.Entry{}
	err := store.WalkSeals(ctx, c.s, c.o.Profile, tenant, c.from, c.last, func(e store.Entry) error {
		if so, ok := store.ParseSealKey(e.Key); ok {
			have[so.Hour] = e
		}
		return nil
	})
	if err != nil {
		return err
	}
	// A key under the tenant's seals that is not in the grammar is a finding of
	// its own, and cannot be found by hour: the whole prefix is listed once for
	// the strays, which is cheap beside what the hours cost.
	all, err := c.s.List(ctx, store.SealTenantPrefix(c.o.Profile, tenant), "", 0)
	if err != nil {
		return err
	}
	for _, e := range all {
		if _, ok := store.ParseSealKey(e.Key); !ok {
			c.report.Findings = append(c.report.Findings, Finding{
				Rule: RuleSealKey, Key: e.Key,
				Detail: "the key is not seals/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>.jws",
			})
		}
	}
	if len(have) == 0 && first.IsZero() {
		return nil
	}

	// The seal before the range, which the first seal in it chains to.
	var prev []byte
	if before, err := c.s.Get(ctx, store.SealKey(c.o.Profile, tenant, c.from.Add(-time.Hour))); err == nil {
		prev = before
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}

	for hour := c.from; !hour.After(c.last); hour = hour.Add(time.Hour) {
		entry, ok := have[hour]
		if !ok {
			if !first.IsZero() && !hour.Before(first) && !hour.After(c.due) {
				c.bad(hour, store.SealKey(c.o.Profile, tenant, hour), RuleSealMissing,
					"tenant %s: there is no seal for the hour, and it closed and settled long enough ago to have one", tenant)
			}
			prev = nil
			continue
		}
		head, err := c.s.Head(ctx, entry.Key)
		if err != nil {
			c.bad(hour, entry.Key, RuleSealJWS, "the seal cannot be read: %v", err)
			prev = nil
			continue
		}
		body, err := c.s.Get(ctx, entry.Key)
		if err != nil {
			c.bad(hour, entry.Key, RuleSealJWS, "the seal cannot be read: %v", err)
			prev = nil
			continue
		}
		c.report.Seals++
		c.report.Hours[hour] = true
		before := len(c.report.Findings)
		c.seal(ctx, tenant, hour, head, body, prev)
		if len(c.report.Findings) == before {
			c.report.Clean = append(c.report.Clean, SealChecked{Key: entry.Key, Hour: hour, Head: head})
		}
		prev = body
	}
	return nil
}

// seal checks one seal.
func (c *sealChecker) seal(ctx context.Context, tenant string, hour time.Time, head store.Entry, body, prev []byte) {
	key := head.Key
	typed, err := seal.ParseSeal(body)
	if err != nil {
		rule := RuleSealJWS
		var payload *seal.PayloadError
		if errors.As(err, &payload) {
			rule = RuleSealPayload
		}
		c.bad(hour, key, rule, "tenant %s: %v", tenant, err)
		return
	}
	s := typed.Seal
	if s.GetProfile() != c.o.Profile || s.GetTenant() != tenant || !s.GetHour().AsTime().Equal(hour) {
		c.bad(hour, key, RuleSealPayload,
			"the key says profile %s, tenant %s, hour %s and the seal says profile %s, tenant %s, hour %s",
			c.o.Profile, tenant, hour.Format(time.RFC3339), s.GetProfile(), s.GetTenant(), s.GetHour().AsTime().Format(time.RFC3339))
	}
	if at := s.GetSealedAt().AsTime(); at.Before(hour.Add(time.Hour)) {
		c.bad(hour, key, RuleSealPayload, "sealed_at %s is before the hour it covers has ended", at.Format(time.RFC3339))
	}
	if err := c.trust.Check(typed, head.Modified); err != nil {
		c.bad(hour, key, RuleSealSignature, "tenant %s: not trusted: %v", tenant, err)
	}

	// The chain: prev is the hash of the seal of the hour before, and the first
	// seal of a tenant has none.
	switch {
	case prev == nil && s.GetPrev() != "":
		c.bad(hour, key, RuleSealChain, "it chains to %s, and the seal for the previous hour is not in the bucket", s.GetPrev())
	case prev != nil && s.GetPrev() != seal.Hash(prev):
		c.bad(hour, key, RuleSealChain, "it chains to %q and the seal for the previous hour hashes to %s", s.GetPrev(), seal.Hash(prev))
	}

	// What the hour holds now, against what the seal says it held.
	now, err := seal.Compute(ctx, c.s, c.o.Profile, tenant, hour, false)
	if err != nil {
		c.bad(hour, key, RuleSealRoot, "the hour's objects cannot be read: %v", err)
		return
	}
	var changed []string
	if now.Count != s.GetCount() {
		changed = append(changed, fmt.Sprintf("it says %d records and the hour holds %d", s.GetCount(), now.Count))
	}
	if now.First != s.GetFirst() || now.Last != s.GetLast() {
		changed = append(changed, fmt.Sprintf("it says the objects run from %q to %q and they run from %q to %q",
			s.GetFirst(), s.GetLast(), now.First, now.Last))
	}
	if m, ok := s.GetMeters()["objects"]; ok && m != int64(now.Objects) {
		changed = append(changed, fmt.Sprintf("it says %d objects and the hour holds %d", m, now.Objects))
	}
	if now.RootHex() != s.GetRoot() {
		changed = append(changed, fmt.Sprintf("its root is %s and the hour's records hash to %s", s.GetRoot(), now.RootHex()))
	}
	if len(changed) > 0 {
		c.bad(hour, key, RuleSealRoot, "tenant %s: the hour no longer holds what was sealed (an object was added, removed or changed): %s",
			tenant, strings.Join(changed, "; "))
	}

	// A seal is locked as long as the records it covers, and a checker that
	// knows the profile demands a lock holds it to that.
	if c.o.Lock != "" && c.o.Lock != "none" {
		switch {
		case head.RetainUntil.IsZero():
			c.bad(hour, key, RuleSealLock, "the seal carries no retention, and the profile demands Object Lock in %s mode", c.o.Lock)
		case head.RetainUntil.Before(now.Retain):
			c.bad(hour, key, RuleSealLock, "the seal's lock ends %s, before the records it covers are released at %s",
				head.RetainUntil.Format(time.RFC3339), now.Retain.Format(time.RFC3339))
		}
	}
}
