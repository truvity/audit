package digest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/truvity/audit/store"
)

// VerifiedPrefix is where verifications are recorded, apart from the digests so
// that the chain's own walk never mistakes one for a window.
const VerifiedPrefix = "verified"

// Verification is one window checked, as the scheduled verification recorded
// it. Append-only, like everything else in the archive: a later verification is
// another object beside the earlier ones, never a replacement.
//
// It is what lets a reader of one record ask "has the copy I am looking at been
// checked, and when" and get an answer, rather than having to run the chain
// themselves.
type Verification struct {
	Profile     string    `json:"profile"`
	Digest      string    `json:"digest"`
	WindowStart time.Time `json:"window_start"`
	VerifiedAt  time.Time `json:"verified_at"`
	OK          bool      `json:"ok"`
	Problems    int       `json:"problems"`
	By          string    `json:"by,omitempty"`
}

// verifiedPrefix is one window's verifications.
func verifiedPrefix(profile string, start time.Time) string {
	start = start.UTC()
	return fmt.Sprintf("%s/profile=%s/year=%s/month=%s/day=%s/hour=%s/",
		VerifiedPrefix, profile, start.Format("2006"), start.Format("01"),
		start.Format("02"), start.Format("15"))
}

// RecordVerification writes one verification. The key sorts by the moment of
// verification, so the last in a listing is the latest.
func RecordVerification(ctx context.Context, s store.Store, v Verification, retainUntil time.Time) error {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	key := verifiedPrefix(v.Profile, v.WindowStart) + v.VerifiedAt.UTC().Format("20060102T150405.000000000Z") + ".json"
	err = s.Put(ctx, store.Object{
		Key: key, Body: append(body, '\n'), RetainUntil: retainUntil, ContentType: "application/json",
	})
	if errors.Is(err, store.ErrExists) {
		return nil
	}
	return err
}

// LatestVerification is the most recent verification of a window, or nil when
// it has never been verified.
func LatestVerification(ctx context.Context, s store.Store, profile string, start time.Time) (*Verification, error) {
	entries, err := s.List(ctx, verifiedPrefix(profile, start), "", 0)
	if err != nil {
		return nil, err
	}
	var latest string
	for _, e := range entries {
		if strings.HasSuffix(e.Key, ".json") && e.Key > latest {
			latest = e.Key
		}
	}
	if latest == "" {
		return nil, nil
	}
	body, err := s.Get(ctx, latest)
	if err != nil {
		return nil, err
	}
	var v Verification
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("verification %s: %w", latest, err)
	}
	return &v, nil
}

// Provenance is what the chain says about one archive object: the digest that
// accounts for it, and when that digest was last verified clean.
type Provenance struct {
	Digest     string
	VerifiedAt time.Time
}

// ProvenanceOf finds the digest accounting for an object and its latest clean
// verification.
//
// A digest covers the hour an object was written in, so the object's own
// write time names the window, and the digest is read to confirm it really
// lists the object — a window that exists but does not name it is not an
// account of it. An object whose hour is not sealed yet, the ordinary state of
// the current hour, has no digest and comes back empty. A verification that
// found problems is not reported as a verification: the answer to "has this
// been checked" must not be yes when the check failed.
func ProvenanceOf(ctx context.Context, s store.Store, profile, objectKey string) (Provenance, error) {
	head, err := s.Head(ctx, objectKey)
	if err != nil {
		return Provenance{}, err
	}
	start := head.Modified.UTC().Truncate(time.Hour)
	key := Key(profile, start)
	d, err := Read(ctx, s, key)
	if errors.Is(err, store.ErrNotFound) {
		return Provenance{}, nil
	}
	if err != nil {
		return Provenance{}, err
	}
	named := false
	for _, o := range d.Objects {
		if o.Key == objectKey {
			named = true
			break
		}
	}
	if !named {
		return Provenance{}, nil
	}
	out := Provenance{Digest: key}
	v, err := LatestVerification(ctx, s, profile, start)
	if err != nil {
		return out, err
	}
	if v != nil && v.OK {
		out.VerifiedAt = v.VerifiedAt
	}
	return out, nil
}
