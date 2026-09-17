// Package digest builds and checks the chain that makes the archive
// tamper-evident.
//
// Object Lock proves that nothing was deleted. It does not prove that nothing
// was omitted, or that an object was written when it claims. An hourly digest
// names every object written in its window with that object's hash, names the
// digest before it with that digest's hash and signature, and is itself signed.
// Following the chain backwards therefore shows that the set of objects has not
// been added to, taken from or altered since each window closed.
//
// A digest is written for a quiet window too. Without that, an hour with no
// digest would be indistinguishable from an hour whose digest was removed, and
// absence is exactly what a chain exists to make provable.
package digest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store"
)

// Version is the digest format this build writes.
const Version = "1"

// Prefix is where digests live, away from the objects they cover so that a
// policy can treat them differently.
const Prefix = "digest"

// Digest is one window of the chain.
type Digest struct {
	DigestVersion string    `json:"digest_version"`
	Profile       string    `json:"profile"`
	WindowStart   time.Time `json:"window_start"`
	WindowEnd     time.Time `json:"window_end"`
	Objects       []Covered `json:"objects"`

	PreviousKey       string `json:"previous_digest_key,omitempty"`
	PreviousSHA256    string `json:"previous_digest_sha256,omitempty"`
	PreviousSignature string `json:"previous_digest_signature,omitempty"`

	SignedBy  string `json:"signed_by"`
	Signature string `json:"signature,omitempty"`
}

// Covered is one object a digest accounts for.
type Covered struct {
	Key         string    `json:"key"`
	SHA256      string    `json:"sha256"`
	Size        int64     `json:"size"`
	RetainUntil time.Time `json:"retain_until,omitempty"`
}

// Builder writes digests.
type Builder struct {
	Store  store.Store
	Signer keys.Signer
	// Lookback is how far back the builder looks for objects written in its
	// window. An object is keyed by when its records happened, not by when it
	// was written, so a record delayed by an outbox lands under an older day.
	// Default 7 days.
	Lookback time.Duration
}

// Build produces the digest of one window for one profile, without writing it.
//
// The window is by *write* time, not by the time the records happened: what a
// chain accounts for is objects appearing in the archive, and a record that
// waited a day in an outbox appears when it appears.
func (b *Builder) Build(ctx context.Context, profile, prefix string, start, end time.Time) (*Digest, error) {
	covered, err := b.covered(ctx, prefix, start, end)
	if err != nil {
		return nil, err
	}
	d := &Digest{
		DigestVersion: Version,
		Profile:       profile,
		WindowStart:   start.UTC(),
		WindowEnd:     end.UTC(),
		Objects:       covered,
		SignedBy:      b.Signer.KeyID(),
	}
	previous, previousKey, err := b.previous(ctx, profile, start)
	if err != nil {
		return nil, err
	}
	if previous != nil {
		body, err := marshal(previous)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		d.PreviousKey = previousKey
		d.PreviousSHA256 = hex.EncodeToString(sum[:])
		d.PreviousSignature = previous.Signature
	}
	return d, nil
}

// Write signs a digest and puts it, returning its key.
func (b *Builder) Write(ctx context.Context, d *Digest, retainUntil time.Time) (string, error) {
	unsigned, err := marshal(d)
	if err != nil {
		return "", err
	}
	signature, err := b.Signer.Sign(ctx, unsigned)
	if err != nil {
		return "", fmt.Errorf("digest: sign: %w", err)
	}
	d.Signature = hex.EncodeToString(signature)

	body, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", fmt.Errorf("digest: %w", err)
	}
	key := Key(d.Profile, d.WindowStart)
	err = b.Store.Put(ctx, store.Object{
		Key: key, Body: append(body, '\n'), RetainUntil: retainUntil,
		ContentType: "application/json",
		Metadata: map[string]string{
			"audit-profile": d.Profile,
			"audit-objects": fmt.Sprint(len(d.Objects)),
		},
	})
	if err != nil {
		return "", err
	}
	return key, nil
}

// covered lists the objects written in the window, hashing each.
func (b *Builder) covered(ctx context.Context, prefix string, start, end time.Time) ([]Covered, error) {
	lookback := b.Lookback
	if lookback <= 0 {
		lookback = 7 * 24 * time.Hour
	}
	var out []Covered
	err := store.WalkDays(ctx, b.Store, prefix, start.Add(-lookback), end, func(e store.Entry) error {
		if e.Modified.Before(start) || !e.Modified.Before(end) {
			return nil
		}
		body, err := b.Store.Get(ctx, e.Key)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		out = append(out, Covered{
			Key: e.Key, SHA256: hex.EncodeToString(sum[:]),
			Size: int64(len(body)), RetainUntil: e.RetainUntil,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// previous finds the digest immediately before a window.
//
// Every window produces a digest, empty ones included, so the previous one is
// almost always the hour before and one head finds it. The listing is for the
// rest: the first window ever, or the one after a gap.
func (b *Builder) previous(ctx context.Context, profile string, start time.Time) (*Digest, string, error) {
	if key := Key(profile, start.Add(-time.Hour)); key != "" {
		if _, err := b.Store.Head(ctx, key); err == nil {
			d, err := Read(ctx, b.Store, key)
			if err != nil {
				return nil, "", err
			}
			return d, key, nil
		}
	}
	entries, err := b.Store.List(ctx, profilePrefix(profile), "", 0)
	if err != nil {
		return nil, "", err
	}
	want := Key(profile, start)
	var best string
	for _, e := range entries {
		if e.Key < want && e.Key > best {
			best = e.Key
		}
	}
	if best == "" {
		return nil, "", nil
	}
	d, err := Read(ctx, b.Store, best)
	if err != nil {
		return nil, "", err
	}
	return d, best, nil
}

// Read loads a digest.
func Read(ctx context.Context, s store.Store, key string) (*Digest, error) {
	body, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	var d Digest
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("digest %s: %w", key, err)
	}
	return &d, nil
}

// Key is where a window's digest lives.
func Key(profile string, start time.Time) string {
	start = start.UTC()
	return fmt.Sprintf("%s/year=%s/month=%s/day=%s/hour=%s.json",
		profilePrefix(profile), start.Format("2006"), start.Format("01"),
		start.Format("02"), start.Format("15"))
}

func profilePrefix(profile string) string { return Prefix + "/profile=" + profile }

// LastWindow is the window of a profile's most recent digest.
//
// It is what lets an hourly job catch up rather than only ever sealing the hour
// it woke in: a job that missed three runs must seal those three windows, or
// the chain has gaps that nothing later can fill. A gap cannot be told from a
// digest somebody removed, which is the whole point of the chain.
func LastWindow(ctx context.Context, s store.Store, profile string) (time.Time, bool, error) {
	entries, err := s.List(ctx, profilePrefix(profile), "", 0)
	if err != nil {
		return time.Time{}, false, err
	}
	var latest time.Time
	var found bool
	for _, e := range entries {
		start, ok := windowOf(e.Key)
		if !ok {
			continue
		}
		if !found || start.After(latest) {
			latest, found = start, true
		}
	}
	return latest, found, nil
}

// marshal is the form a signature covers: the digest without its own signature,
// canonical, so that signing and checking cannot disagree about whitespace or
// key order.
func marshal(d *Digest) ([]byte, error) {
	unsigned := *d
	unsigned.Signature = ""
	body, err := json.Marshal(unsigned)
	if err != nil {
		return nil, fmt.Errorf("digest: %w", err)
	}
	return record.CanonicalJSON(body)
}

var errNoSigner = errors.New("digest: a signer is required")

// Check holds a builder to its parts.
func (b *Builder) Check() error {
	if b.Store == nil {
		return errors.New("digest: a store is required")
	}
	if b.Signer == nil {
		return errNoSigner
	}
	return nil
}
