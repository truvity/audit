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

	"github.com/truvity/audit/internal/bucketcontract"
	"github.com/truvity/audit/sdk/catalogue"
	auditv1 "github.com/truvity/audit/sdk/gen/audit/v1"
	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/sdk/sink"
	"github.com/truvity/audit/store"
)

// Verify checks a profile's record objects over a range of ingest time against
// the bucket contract (docs/reference/bucket-contract.md): for every object, its
// key, its metadata, the sha256 of its stored bytes, and the hash on every one
// of its records.
//
// It needs the archive and nothing else. An auditor runs it with read-only
// credentials and their own copy of this command, which is what makes the
// answer worth having: nothing in the result depends on trusting the operator
// of the archive. What the objects alone cannot say is that nothing was omitted
// or added: that is what seals vouch for (docs/decisions/0019-seals.md), and
// with Seals set this checks them too, against the roots it is given and
// nothing else.
type Verify struct {
	Store   store.Store
	Profile string
	// From and To bound the range, of ingest time: the hours from the one From
	// is in up to but not including the hour To is in (an exact hour boundary
	// is excluded).
	From, To time.Time
	// MinimumRetention lets the check also say whether an object's lock is
	// shorter than its profile requires.
	MinimumRetention map[string]time.Duration
	// RequiredLock is the lock mode each profile demands, from the composed
	// deployment when the command was given one. With it an object with no
	// lock is INVALID under a profile that demands one and `unlocked` under
	// one that does not; without it locks are not spoken of.
	RequiredLock map[string]string
	// Sink and Catalogue, when both are given, are where this job records what
	// it checked. A verification that never ran and one that found nothing
	// wrong look identical in the archive; these events are the difference.
	Sink      sink.Sink
	Catalogue *catalogue.Catalogue
	Version   string
	Instance  string
	JSON      bool
	Out       io.Writer
	// Seals, when set, makes the check cover the seals of the range too.
	Seals *SealCheck
	// Now is the clock the missing-seal rule reads; nil is the wall clock.
	Now func() time.Time
}

// SealCheck is what a verifier needs to check seals: the roots it trusts, and
// when a seal is late enough that its absence is a fault.
type SealCheck struct {
	// Roots are the thumbprints of the root keys the verifier pins. Nothing else
	// in the bucket is trusted, however it is signed.
	Roots []string
	// Settle is the notary's settle window and Grace how much later than that a
	// seal may still be missing: an hour is due once it has ended, Settle has
	// passed and Grace has passed.
	Settle, Grace time.Duration
}

// VerifyFinding is one thing wrong, or one object checked and right.
type VerifyFinding struct {
	// Window is the hour of ingest time the object is in, as the prefix of its
	// hour: records/<profile>/<yyyy>/<mm>/<dd>/<hh>.
	Window string `json:"window"`
	Object string `json:"object,omitempty"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	// Unlocked is an object that is right and carries no retention, under a
	// profile that demands no lock: information, not a problem.
	Unlocked bool `json:"unlocked,omitempty"`
}

// VerifyReport is what a verification found.
type VerifyReport struct {
	Profile string    `json:"profile"`
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Objects int       `json:"objects"`
	Records int       `json:"records"`
	// Seals is how many seals were checked; zero when seals were not.
	Seals int `json:"seals,omitempty"`
	// Windows are the hours that had objects, in the order they were checked.
	// A caller that reports per window needs to know which were looked at, not
	// only which had something wrong.
	Windows  []string        `json:"windows,omitempty"`
	Findings []VerifyFinding `json:"findings"`
}

// Problems returns only what was wrong.
func (r *VerifyReport) Problems() []VerifyFinding {
	var out []VerifyFinding
	for _, f := range r.Findings {
		if !f.OK {
			out = append(out, f)
		}
	}
	return out
}

// Unlocked counts the objects that are right and carry no lock.
func (r *VerifyReport) Unlocked() int {
	n := 0
	for _, f := range r.Findings {
		if f.OK && f.Unlocked {
			n++
		}
	}
	return n
}

// String renders the report as the command prints it.
func (r *VerifyReport) String() string {
	var b strings.Builder
	for _, f := range r.Findings {
		where := f.Window
		if f.Object != "" {
			where = f.Object
		}
		switch {
		case f.OK && f.Unlocked:
			fmt.Fprintf(&b, "unlocked %s\n", where)
		case f.OK:
			fmt.Fprintf(&b, "valid    %s\n", where)
		default:
			fmt.Fprintf(&b, "INVALID  %s: %s\n", where, f.Reason)
		}
	}
	fmt.Fprintf(&b, "%d objects, %d records", r.Objects, r.Records)
	if n := r.Unlocked(); n > 0 {
		fmt.Fprintf(&b, ", %d unlocked", n)
	}
	fmt.Fprintf(&b, ", %d problems\n", len(r.Problems()))
	return b.String()
}

// Run reports the number of problems found.
func (v Verify) Run(ctx context.Context) (int, error) {
	out := v.Out
	if out == nil {
		out = os.Stdout
	}
	report, err := v.verify(ctx)
	if err != nil {
		return 0, err
	}
	if err := v.record(ctx, report); err != nil {
		return 0, err
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

func (v Verify) verify(ctx context.Context) (*VerifyReport, error) {
	report := &VerifyReport{Profile: v.Profile, From: v.From.UTC(), To: v.To.UTC()}
	if why := store.KeyComponent(v.Profile); why != "" {
		return nil, fmt.Errorf("verify: the profile %q %s", v.Profile, why)
	}
	// [From, To): the last hour is the one before To's, unless To is not on an
	// hour, in which case it is To's own.
	last := v.To.UTC().Add(-time.Nanosecond)

	windows := map[string]bool{}
	tenants, err := store.Tenants(ctx, v.Store, v.Profile)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	for _, tenant := range tenants {
		err := store.WalkHours(ctx, v.Store, v.Profile, tenant, v.From, last, "", func(e store.Entry) error {
			window := windowOf(e.Key, v.Profile)
			if !windows[window] {
				windows[window] = true
				report.Windows = append(report.Windows, window)
			}
			report.Objects++
			report.Findings = append(report.Findings, v.object(ctx, report, tenant, window, e)...)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("verify: %w", err)
		}
	}
	if v.Seals != nil {
		if err := v.checkSeals(ctx, report, windows); err != nil {
			return nil, fmt.Errorf("verify: %w", err)
		}
	}
	// Windows come tenant by tenant; a window is reported in time order.
	sort.Strings(report.Windows)
	return report, nil
}

// windowOf is the hour prefix of a record key, without the trailing slash and
// without the tenant: the unit a person asks about and a report names.
func windowOf(key, profile string) string {
	if o, ok := store.ParseRecordKey(key); ok {
		return store.ProfilePrefix(profile) + o.Hour.Format("2006/01/02/15")
	}
	return key
}

// object checks one object and returns what it found: one valid finding, or
// one invalid finding per rule broken. What the contract says of an object is
// checked by internal/bucketcontract, which is also what the conformance suite
// holds the writer to; the lock is the deployment's and is checked here.
func (v Verify) object(ctx context.Context, report *VerifyReport, tenant, window string, e store.Entry) []VerifyFinding {
	res := bucketcontract.CheckObject(ctx, v.Store, e.Key, v.Profile, tenant)
	report.Records += res.Records
	if len(res.Findings) > 0 {
		out := make([]VerifyFinding, 0, len(res.Findings))
		for _, f := range res.Findings {
			out = append(out, VerifyFinding{Window: window, Object: e.Key, Reason: f.Rule + ": " + f.Detail})
		}
		return out
	}
	return []VerifyFinding{v.lock(window, res.Entry)}
}

// lock reads the object's lock, when the verifier knows what the profile
// requires of it, and says what it found.
func (v Verify) lock(window string, entry store.Entry) VerifyFinding {
	profile := v.Profile
	want, wantRetention := v.MinimumRetention[profile]
	lock, wantLock := v.RequiredLock[profile]
	if !wantRetention && !wantLock {
		return VerifyFinding{Window: window, Object: entry.Key, OK: true}
	}
	if entry.RetainUntil.IsZero() {
		// No retention. A profile that demands a lock has an object that is
		// deletable, which is the one thing it forbids; one that demands none
		// has an object its own hashes alone vouch for, and says so.
		if wantLock && lock != "none" {
			return VerifyFinding{
				Window: window, Object: entry.Key,
				Reason: fmt.Sprintf("the object carries no retention, and profile %s demands Object Lock in %s mode", profile, lock),
			}
		}
		return VerifyFinding{Window: window, Object: entry.Key, OK: true, Unlocked: wantLock}
	}
	if wantRetention && entry.RetainUntil.Before(entry.Modified.Add(want)) {
		return VerifyFinding{
			Window: window, Object: entry.Key,
			Reason: fmt.Sprintf("the lock ends %s, sooner than profile %s requires", entry.RetainUntil, profile),
		}
	}
	return VerifyFinding{Window: window, Object: entry.Key, OK: true}
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
// Per window rather than per run, because that is the useful grain: an
// operator asked which hour is in doubt, not whether last night was clean. A
// window with nothing wrong is recorded too, since a verification that never
// ran and one that found nothing wrong are indistinguishable otherwise.
//
// The events are the common catalogue's audit.seal.verified and
// audit.seal.failed, and they say what they name: they are recorded only when
// seals were checked. A run that checked objects alone has said nothing about
// any seal, and a record that claimed it had would be the false assurance this
// exists to prevent. The window is named as the prefix of its hour.
func (v Verify) record(ctx context.Context, report *VerifyReport) error {
	if v.Seals == nil {
		return nil
	}
	reporter, err := newReporter(v.Catalogue, v.Sink, v.Version, v.instance())
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if reporter == nil {
		return nil
	}
	defer reporter.close()

	reasons := map[string][]string{}
	var order []string
	for _, f := range report.Problems() {
		if _, seen := reasons[f.Window]; !seen {
			order = append(order, f.Window)
		}
		reason := f.Reason
		if f.Object != "" {
			reason = f.Object + ": " + reason
		}
		reasons[f.Window] = append(reasons[f.Window], reason)
	}

	for _, window := range report.Windows {
		if _, bad := reasons[window]; bad {
			continue
		}
		reporter.record(ctx, succeeded(
			reporter.event("audit.seal.verified", "seal", window),
			auditv1.Operation_OPERATION_ACCESS))
	}
	for _, window := range order {
		reporter.record(ctx, failed(
			reporter.event("audit.seal.failed", "seal", window),
			auditv1.Operation_OPERATION_ACCESS,
			strings.Join(reasons[window], "; ")))
	}
	return nil
}

func (v Verify) instance() string {
	if v.Instance != "" {
		return v.Instance
	}
	return record.InstanceName()
}
