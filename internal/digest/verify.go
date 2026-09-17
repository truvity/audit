package digest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/store"
)

// Finding is one thing wrong, or one thing checked and right.
type Finding struct {
	Digest string `json:"digest"`
	Object string `json:"object,omitempty"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// Report is what a verification found.
type Report struct {
	Profile  string    `json:"profile"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Digests  int       `json:"digests"`
	Objects  int       `json:"objects"`
	Findings []Finding `json:"findings"`
}

// OK reports whether everything checked out.
func (r *Report) OK() bool {
	for _, f := range r.Findings {
		if !f.OK {
			return false
		}
	}
	return true
}

// Problems returns only what was wrong.
func (r *Report) Problems() []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if !f.OK {
			out = append(out, f)
		}
	}
	return out
}

// String renders the report as the command prints it.
func (r *Report) String() string {
	var b strings.Builder
	for _, f := range r.Findings {
		where := f.Digest
		if f.Object != "" {
			where = f.Object
		}
		if f.OK {
			fmt.Fprintf(&b, "valid    %s\n", where)
			continue
		}
		fmt.Fprintf(&b, "INVALID  %s: %s\n", where, f.Reason)
	}
	fmt.Fprintf(&b, "%d digests, %d objects, %d problems\n", r.Digests, r.Objects, len(r.Problems()))
	return b.String()
}

// Verifier checks a chain. It needs the store and a public key and nothing
// else: an auditor runs it with read-only credentials, which is what makes the
// answer worth having.
type Verifier struct {
	Store        store.Store
	PublicKeyPEM []byte
	// Profiles is the retention each profile requires, so that an object whose
	// lock is shorter than its profile asks can be reported. Optional.
	MinimumRetention map[string]time.Duration
	// Lookback is how far before the range to look for objects written in it
	// but keyed under an older day, which an outbox delay produces. It wants to
	// be at least the builder's, or an object the builder covered from further
	// back is not looked at here. Default 7 days.
	Lookback time.Duration
}

// Verify walks a profile's chain from newest to oldest across a range.
//
// Newest first, because that is the order in which a doubt arises: someone asks
// whether what is there now is what was written, and the answer is built
// backwards from the most recent digest until the range is covered.
func (v *Verifier) Verify(ctx context.Context, profile string, from, to time.Time) (*Report, error) {
	report := &Report{Profile: profile, From: from.UTC(), To: to.UTC()}

	entries, err := v.Store.List(ctx, profilePrefix(profile), "", 0)
	if err != nil {
		return nil, err
	}
	keysInRange := make([]string, 0, len(entries))
	for _, e := range entries {
		if start, ok := windowOf(e.Key); ok && !start.Before(from) && start.Before(to) {
			keysInRange = append(keysInRange, e.Key)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keysInRange)))

	if len(keysInRange) == 0 {
		report.Findings = append(report.Findings, Finding{
			Digest: profilePrefix(profile),
			Reason: "no digest covers this range: an hour with no digest cannot be told from one whose digest was removed",
		})
		return report, nil
	}

	covered := map[string]bool{}
	for _, key := range keysInRange {
		report.Digests++
		d, err := Read(ctx, v.Store, key)
		if err != nil {
			report.Findings = append(report.Findings, Finding{Digest: key, Reason: err.Error()})
			continue
		}
		report.Findings = append(report.Findings, v.checkDigest(ctx, key, d)...)
		for _, o := range d.Objects {
			covered[o.Key] = true
			report.Objects++
			report.Findings = append(report.Findings, v.checkObject(ctx, key, profile, o))
		}
	}

	// An object nothing accounts for is the other half of the question: the
	// chain shows that what it names is unaltered, and this shows that nothing
	// was added beside it.
	report.Findings = append(report.Findings, v.checkUncovered(ctx, profile, from, to, covered)...)
	return report, nil
}

func (v *Verifier) checkDigest(ctx context.Context, key string, d *Digest) []Finding {
	var findings []Finding

	unsigned, err := marshal(d)
	if err != nil {
		return []Finding{{Digest: key, Reason: err.Error()}}
	}
	signature, err := hex.DecodeString(d.Signature)
	if err != nil {
		findings = append(findings, Finding{Digest: key, Reason: "the signature is not hex"})
	} else if err := keys.Verify(v.PublicKeyPEM, unsigned, signature); err != nil {
		findings = append(findings, Finding{Digest: key, Reason: err.Error()})
	} else {
		findings = append(findings, Finding{Digest: key, OK: true})
	}

	if d.PreviousKey == "" {
		return findings
	}
	previous, err := Read(ctx, v.Store, d.PreviousKey)
	if err != nil {
		return append(findings, Finding{
			Digest: key,
			Reason: fmt.Sprintf("the digest before it, %s, is missing: %v", d.PreviousKey, err),
		})
	}
	body, err := marshal(previous)
	if err != nil {
		return append(findings, Finding{Digest: key, Reason: err.Error()})
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != d.PreviousSHA256 {
		findings = append(findings, Finding{
			Digest: key,
			Reason: fmt.Sprintf("the digest before it has changed: %s names %s, found %s", key, d.PreviousSHA256, got),
		})
	}
	if previous.Signature != d.PreviousSignature {
		findings = append(findings, Finding{
			Digest: key,
			Reason: "the signature of the digest before it does not match what this one records",
		})
	}
	return findings
}

func (v *Verifier) checkObject(ctx context.Context, digestKey, profile string, o Covered) Finding {
	body, err := v.Store.Get(ctx, o.Key)
	if err != nil {
		return Finding{Digest: digestKey, Object: o.Key, Reason: "missing: " + err.Error()}
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != o.SHA256 {
		return Finding{Digest: digestKey, Object: o.Key, Reason: "the object has changed since it was signed"}
	}
	if want, ok := v.MinimumRetention[profile]; ok {
		entry, err := v.Store.Head(ctx, o.Key)
		if err == nil && !entry.RetainUntil.IsZero() {
			if entry.RetainUntil.Before(entry.Modified.Add(want)) {
				return Finding{
					Digest: digestKey, Object: o.Key,
					Reason: fmt.Sprintf("the lock ends %s, sooner than profile %s requires", entry.RetainUntil, profile),
				}
			}
		}
	}
	return Finding{Digest: digestKey, Object: o.Key, OK: true}
}

// checkUncovered looks for objects in the range that no digest names.
func (v *Verifier) checkUncovered(
	ctx context.Context, profile string, from, to time.Time, covered map[string]bool,
) []Finding {
	lookback := v.Lookback
	if lookback <= 0 {
		lookback = 7 * 24 * time.Hour
	}
	prefix := "profile=" + profile
	var findings []Finding
	err := store.WalkDays(ctx, v.Store, prefix, from.Add(-lookback), to, func(e store.Entry) error {
		if e.Modified.Before(from) || !e.Modified.Before(to) {
			return nil
		}
		if !covered[e.Key] {
			findings = append(findings, Finding{
				Object: e.Key,
				Reason: "no digest accounts for this object",
			})
		}
		return nil
	})
	if err != nil {
		return []Finding{{Digest: prefix, Reason: err.Error()}}
	}
	return findings
}

// windowOf reads the window a digest key names.
func windowOf(key string) (time.Time, bool) {
	var year, month, day, hour int
	i := strings.Index(key, "/year=")
	if i < 0 {
		return time.Time{}, false
	}
	if _, err := fmt.Sscanf(key[i:], "/year=%4d/month=%2d/day=%2d/hour=%2d.json",
		&year, &month, &day, &hour); err != nil {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), day, hour, 0, 0, 0, time.UTC), true
}
