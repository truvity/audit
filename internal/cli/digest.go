package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/truvity/audit/internal/digest"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/preset"
	"github.com/truvity/audit/store"
)

// Digest seals windows of the archive into the signed chain.
//
// Object Lock proves nothing was deleted. The chain proves nothing was omitted
// or altered: each hour names every object written in it with its hash, and
// names the previous digest, its hash and its signature. An auditor with the
// public key and read access can check the whole of it without trusting whoever
// operates the archive.
//
// It is a command rather than something the writer does, because sealing is a
// different privilege from writing — the signing key must not live where the
// writer's credentials do — and because a window is sealed once for the whole
// deployment however many writers filled it.
type Digest struct {
	Store    store.Store
	Signer   keys.Signer
	Profiles map[string]*preset.Profile
	// From and To bound the windows to seal. A zero To means the last hour that
	// has closed: an hour still in progress would be sealed without the objects
	// still to be written into it. A zero From means resume from the hour after
	// each profile's last digest, so a job that missed its runs catches up
	// rather than leaving gaps nothing can fill later.
	From, To time.Time
	// Lookback is how far back to look for objects written into this window but
	// keyed under an older day, which an outbox delay produces.
	Lookback time.Duration
	// MaxWindows bounds one run. A job that has been down for a month has a
	// month of windows to seal, and an operator would rather see it make
	// progress and say how much is left than watch it run all night.
	// Default 168, a week.
	MaxWindows int
	Now        func() time.Time
	JSON       bool
	Out        io.Writer
}

// DigestReport is what a run sealed.
type DigestReport struct {
	Profiles []DigestProfile `json:"profiles"`
}

// DigestProfile is one profile's part of a run.
type DigestProfile struct {
	Profile string `json:"profile"`
	// Sealed is how many windows this run wrote.
	Sealed int `json:"sealed"`
	// Existing is how many were already sealed, which is the ordinary case when
	// a job runs twice.
	Existing int `json:"existing"`
	// Objects is how many archive objects the new digests account for.
	Objects int `json:"objects"`
	// Remaining is how many windows are still behind, when a run hit its bound.
	Remaining int `json:"remaining,omitempty"`
}

// String is the human form.
func (r DigestReport) String() string {
	var b strings.Builder
	for _, p := range r.Profiles {
		fmt.Fprintf(&b, "profile %s: sealed %d window(s) over %d object(s)",
			p.Profile, p.Sealed, p.Objects)
		if p.Existing > 0 {
			fmt.Fprintf(&b, ", %d already sealed", p.Existing)
		}
		if p.Remaining > 0 {
			fmt.Fprintf(&b, ", %d still behind", p.Remaining)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Run seals the windows of every profile it was given.
func (d Digest) Run(ctx context.Context) (DigestReport, error) {
	out := d.Out
	if out == nil {
		out = os.Stdout
	}
	report := DigestReport{}
	if d.Signer == nil {
		return report, errors.New("digest: a signer is required: an unsigned chain proves nothing")
	}
	builder := &digest.Builder{Store: d.Store, Signer: d.Signer, Lookback: d.Lookback}

	names := make([]string, 0, len(d.Profiles))
	for name := range d.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		sealed, err := d.profile(ctx, builder, name, d.Profiles[name])
		if err != nil {
			return report, err
		}
		report.Profiles = append(report.Profiles, sealed)
	}

	if d.JSON {
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return report, err
		}
		printf(out, "%s\n", body)
	} else {
		printf(out, "%s", report.String())
	}
	return report, nil
}

func (d Digest) profile(
	ctx context.Context, builder *digest.Builder, name string, p *preset.Profile,
) (DigestProfile, error) {
	result := DigestProfile{Profile: name}
	now := d.now()

	to := d.To.UTC().Truncate(time.Hour)
	if d.To.IsZero() {
		to = now.Truncate(time.Hour)
	}
	from, err := d.start(ctx, name, to)
	if err != nil {
		return result, err
	}

	// An hour that has not closed is not sealed: the objects of the hour the
	// job woke in are still being written.
	windows := 0
	for window := from; window.Before(to); window = window.Add(time.Hour) {
		if windows >= d.maxWindows() {
			result.Remaining = int(to.Sub(window) / time.Hour)
			break
		}
		windows++

		key := digest.Key(name, window)
		switch _, err := d.Store.Head(ctx, key); {
		case err == nil:
			// A job runs twice more often than anybody plans for, and a second
			// put would be refused by the archive anyway.
			result.Existing++
			continue
		case !errors.Is(err, store.ErrNotFound):
			return result, fmt.Errorf("digest: %s: %w", key, err)
		}

		built, err := builder.Build(ctx, name, p.Prefix, window, window.Add(time.Hour))
		if err != nil {
			return result, fmt.Errorf("digest: %s %s: %w", name, window.Format(time.RFC3339), err)
		}
		// The digest is kept as long as what it accounts for: a chain whose
		// links expire before the objects proves nothing about what is left.
		if _, err := builder.Write(ctx, built, p.RetainUntil(now, nil)); err != nil {
			if errors.Is(err, store.ErrExists) {
				// Another runner sealed it between the head and the put.
				result.Existing++
				continue
			}
			return result, fmt.Errorf("digest: %s %s: %w", name, window.Format(time.RFC3339), err)
		}
		result.Sealed++
		result.Objects += len(built.Objects)
	}
	return result, nil
}

// start is the first window a run seals.
func (d Digest) start(ctx context.Context, name string, to time.Time) (time.Time, error) {
	if !d.From.IsZero() {
		return d.From.UTC().Truncate(time.Hour), nil
	}
	last, found, err := digest.LastWindow(ctx, d.Store, name)
	if err != nil {
		return time.Time{}, fmt.Errorf("digest: %s: %w", name, err)
	}
	if !found {
		// Nothing has ever been sealed, so seal the hour that just closed
		// rather than every hour since the archive began.
		return to.Add(-time.Hour), nil
	}
	return last.Add(time.Hour), nil
}

func (d Digest) maxWindows() int {
	if d.MaxWindows > 0 {
		return d.MaxWindows
	}
	return 168
}

func (d Digest) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}
