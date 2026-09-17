package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/truvity/audit/index"
	"github.com/truvity/audit/preset"
)

// Marker is the part of the deduplication store a purge needs.
type Marker interface {
	Purge(ctx context.Context, before time.Time) error
}

// Purge brings the index and the deduplication table back within what the
// profiles allow.
//
// Nothing here touches the archive. Those objects are under a compliance lock
// and are released by the lock expiring, which is the point: a retention that
// code could shorten would not be a retention. This keeps the projection from
// answering questions the archive no longer can, and keeps two tables that
// would otherwise grow without end from doing so.
type Purge struct {
	Index    index.Indexer
	Dedupe   Marker
	Profiles map[string]*preset.Profile
	// IdentifyingAfter is how long the index keeps who an event happened to,
	// as opposed to what happened. It has no default and no framework number
	// behind it: the presets this repository ships cite retention for the
	// record, and none of them states a separate, shorter life for the actor
	// and subject columns. Left at zero, nothing is forgotten early — which is
	// not the same as nothing needing to be.
	IdentifyingAfter time.Duration
	// DedupeWindow is how long a written identifier is remembered. It wants to
	// be the widest window the deployment's profiles ask for; shorter and a
	// redelivery arrives after the memory of it has gone.
	DedupeWindow time.Duration
	// DryRun reports what would be purged and purges nothing.
	DryRun bool
	Now    func() time.Time
	JSON   bool
	Out    io.Writer
}

// PurgeReport is what a run forgot, or would have.
type PurgeReport struct {
	DryRun   bool           `json:"dry_run,omitempty"`
	Profiles []PurgeProfile `json:"profiles"`
	// Dedupe is the moment before which written identifiers were forgotten.
	Dedupe *time.Time `json:"dedupe_before,omitempty"`
}

// PurgeProfile is one profile's schedule.
type PurgeProfile struct {
	Profile string `json:"profile"`
	// Everything is the moment before which rows were removed, derived from the
	// profile's own retention.
	Everything time.Time `json:"everything_before"`
	// Identifying is the moment before which the actor, subject, address and
	// correlation columns were cleared, when a deployment sets a schedule.
	Identifying *time.Time `json:"identifying_before,omitempty"`
}

// String is the human form.
func (r PurgeReport) String() string {
	var b strings.Builder
	if r.DryRun {
		b.WriteString("dry run: nothing was purged\n")
	}
	for _, p := range r.Profiles {
		fmt.Fprintf(&b, "profile %s: rows before %s\n",
			p.Profile, p.Everything.Format(time.RFC3339))
		if p.Identifying != nil {
			fmt.Fprintf(&b, "  identifying columns before %s\n", p.Identifying.Format(time.RFC3339))
		}
	}
	if r.Dedupe != nil {
		fmt.Fprintf(&b, "deduplication: identifiers before %s\n", r.Dedupe.Format(time.RFC3339))
	}
	return b.String()
}

// Run purges each profile and then the deduplication table.
func (r Purge) Run(ctx context.Context) (PurgeReport, error) {
	out := r.Out
	if out == nil {
		out = os.Stdout
	}
	now := r.now()
	report := PurgeReport{DryRun: r.DryRun}

	names := make([]string, 0, len(r.Profiles))
	for name := range r.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		p := r.Profiles[name]
		// The profile says how long a copy written now is kept; a row is past
		// its retention when it was recorded that long ago.
		retention := p.RetainUntil(now, nil).Sub(now)
		schedule := PurgeProfile{Profile: name, Everything: now.Add(-retention)}
		if r.IdentifyingAfter > 0 {
			at := now.Add(-r.IdentifyingAfter)
			schedule.Identifying = &at
		}
		report.Profiles = append(report.Profiles, schedule)

		if r.DryRun {
			continue
		}
		// Who it happened to goes first. Were the order reversed, a row past
		// both schedules would be cleared and then removed, which is the same
		// answer for twice the work.
		if schedule.Identifying != nil {
			if err := r.Index.Purge(ctx, name, *schedule.Identifying, index.Identifying); err != nil {
				return report, fmt.Errorf("purge: %s: %w", name, err)
			}
		}
		if err := r.Index.Purge(ctx, name, schedule.Everything, index.Everything); err != nil {
			return report, fmt.Errorf("purge: %s: %w", name, err)
		}
	}

	if r.Dedupe != nil && r.DedupeWindow > 0 {
		before := now.Add(-r.DedupeWindow)
		report.Dedupe = &before
		if !r.DryRun {
			if err := r.Dedupe.Purge(ctx, before); err != nil {
				return report, fmt.Errorf("purge: deduplication: %w", err)
			}
		}
	}

	if r.JSON {
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

func (r Purge) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
