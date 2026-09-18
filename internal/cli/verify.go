package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"strings"

	"github.com/truvity/audit/catalogue"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/internal/digest"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store"
)

// Verify walks a profile's digest chain and reports what it found.
//
// It needs the archive and a public key and nothing else. An auditor runs it
// with read-only credentials and their own copy of this command, which is what
// makes the answer worth having: nothing in the result depends on trusting the
// operator of the archive.
type Verify struct {
	Store        store.Store
	PublicKeyPEM []byte
	Profile      string
	From, To     time.Time
	// MinimumRetention lets the check also say whether an object's lock is
	// shorter than its profile requires.
	MinimumRetention map[string]time.Duration
	// Lookback is how far before the range to look for objects written in it
	// but keyed under an older day. It wants to be at least what the digest job
	// used, or an object the job covered from further back is not looked at.
	Lookback time.Duration
	// Sink and Catalogue, when both are given, are where this job records what
	// it checked. A verification that never ran and one that found nothing
	// wrong look identical in the archive; these events are the difference.
	Sink      sink.Sink
	Catalogue *catalogue.Catalogue
	Version   string
	Instance  string
	// Record writes one verification per window checked into the archive,
	// where Get reads it back as a record's verified_at. It needs write access
	// to verified/, which is why it is asked for: an auditor running this
	// with read-only credentials checks the chain without recording anything.
	Record bool
	Now    func() time.Time
	JSON   bool
	Out    io.Writer
}

// Run reports the number of problems found.
func (v Verify) Run(ctx context.Context) (int, error) {
	out := v.Out
	if out == nil {
		out = os.Stdout
	}
	verifier := &digest.Verifier{
		Store:            v.Store,
		PublicKeyPEM:     v.PublicKeyPEM,
		MinimumRetention: v.MinimumRetention,
		Lookback:         v.Lookback,
	}
	report, err := verifier.Verify(ctx, v.Profile, v.From, v.To)
	if err != nil {
		return 0, err
	}
	if err := v.record(ctx, report); err != nil {
		return 0, err
	}
	if v.Record {
		if err := v.keep(ctx, report); err != nil {
			return 0, err
		}
	}
	if v.JSON {
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return 0, err
		}
		printf(out, "%s\n", body)
	} else {
		printf(out, "%s", report.String())
	}
	return len(report.Problems()), nil
}

// ParseDay reads a date or a timestamp, so that an auditor may write either.
func ParseDay(v string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15", "2006-01-02"} {
		if at, err := time.Parse(layout, v); err == nil {
			return at.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a date or a timestamp", v)
}

// record puts the outcome of each window checked into the trail.
//
// Per window rather than per run, because that is what the catalogue's message
// says and because it is the useful grain: an operator asked which hour is in
// doubt, not whether last night was clean. A window with nothing wrong is
// recorded too — a verification that never ran and one that found nothing wrong
// are indistinguishable otherwise, and the second is the whole point of running
// it nightly.
func (v Verify) record(ctx context.Context, report *digest.Report) error {
	reporter, err := newReporter(v.Catalogue, v.Sink, v.Version, v.instance())
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if reporter == nil {
		return nil
	}
	defer reporter.close()

	// A finding names the window it belongs to, including an uncovered object,
	// which names the window that should have covered it.
	reasons := map[string][]string{}
	order := []string{}
	for _, f := range report.Problems() {
		where := f.Digest
		if where == "" {
			where = report.Profile
		}
		if _, seen := reasons[where]; !seen {
			order = append(order, where)
		}
		reason := f.Reason
		if f.Object != "" {
			reason = f.Object + ": " + reason
		}
		reasons[where] = append(reasons[where], reason)
	}

	for _, window := range report.Windows {
		if _, bad := reasons[window]; bad {
			continue
		}
		reporter.record(ctx, succeeded(
			reporter.event("audit.digest.verified", "digest", window),
			auditv1.Operation_OPERATION_ACCESS))
	}
	for _, window := range order {
		reporter.record(ctx, failed(
			reporter.event("audit.digest.failed", "digest", window),
			auditv1.Operation_OPERATION_ACCESS,
			strings.Join(reasons[window], "; ")))
	}
	return nil
}

// keep writes a verification for each window checked, clean or not.
//
// Each is kept as long as the digest it verifies — read off that digest's own
// lock — so a verification never outlives, or dies before, what it is about.
func (v Verify) keep(ctx context.Context, report *digest.Report) error {
	problems := map[string]int{}
	for _, f := range report.Problems() {
		problems[f.Digest]++
	}
	now := time.Now().UTC()
	if v.Now != nil {
		now = v.Now().UTC()
	}
	for _, window := range report.Windows {
		start, ok := digest.WindowOf(window)
		if !ok {
			continue
		}
		head, err := v.Store.Head(ctx, window)
		if err != nil {
			return fmt.Errorf("verify: recording %s: %w", window, err)
		}
		retain := head.RetainUntil
		if retain.IsZero() {
			retain = now.AddDate(10, 0, 0)
		}
		if err := digest.RecordVerification(ctx, v.Store, digest.Verification{
			Profile: report.Profile, Digest: window, WindowStart: start,
			VerifiedAt: now, OK: problems[window] == 0, Problems: problems[window],
			By: v.instance(),
		}, retain); err != nil {
			return fmt.Errorf("verify: recording %s: %w", window, err)
		}
	}
	return nil
}

func (v Verify) instance() string {
	if v.Instance != "" {
		return v.Instance
	}
	return record.InstanceName()
}
