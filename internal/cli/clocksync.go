package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/truvity/audit/catalogue"
	"github.com/truvity/audit/emit"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/internal/clock"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// ClockSync compares this machine's clock with UTC and records the answer.
//
// ETSI EN 319 401 §7.10 asks that the clock be synchronised and that the
// synchronisation be recorded. The recording is the half that belongs here:
// every timestamp in the archive is this machine's opinion of the time, and an
// auditor who cannot see that the opinion was checked has to take the ordering
// of the whole trail on faith.
//
// It does not set the clock. Whatever runs the machine does that, and a
// component that both set the time and recorded the times of things would be
// marking its own paper.
type ClockSync struct {
	// Sink is the writer. Without one the check runs and reports and records
	// nothing, which is a way to test the references and not a way to satisfy
	// the requirement.
	Sink sink.Sink
	// Catalogue describes audit.clock.synchronised. It is the common one.
	Catalogue *catalogue.Catalogue
	// Servers are the time references, tried in order of who answers.
	Servers []string
	// MaxOffset is how far out the clock may be before the run is a failure the
	// deployment hears about. Zero means any offset is accepted and only
	// recorded.
	MaxOffset time.Duration
	Timeout   time.Duration
	Version   string
	Instance  string
	JSON      bool
	Out       io.Writer
}

// ClockReport is what the check found.
type ClockReport struct {
	Source   string `json:"source"`
	OffsetMS int64  `json:"offset_ms"`
	DelayMS  int64  `json:"delay_ms"`
	Stratum  uint8  `json:"stratum"`
	// Within is false when the offset exceeds what the deployment allows.
	Within bool `json:"within_tolerance"`
	// Recorded says whether the event reached a writer. A check nobody can see
	// the result of has not met the requirement, however good the offset was.
	Recorded bool `json:"recorded"`
	// Unreachable names the references that did not answer. One is survivable
	// and worth seeing; all of them is the error above.
	Unreachable []string `json:"unreachable,omitempty"`
}

// String is the human form.
func (r ClockReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "clock: %+d ms against %s (stratum %d, round trip %d ms)\n",
		r.OffsetMS, r.Source, r.Stratum, r.DelayMS)
	for _, u := range r.Unreachable {
		fmt.Fprintf(&b, "  no answer: %s\n", u)
	}
	if !r.Within {
		fmt.Fprintf(&b, "  the offset is outside what this deployment allows\n")
	}
	if !r.Recorded {
		fmt.Fprintf(&b, "  not recorded: the check ran but nothing in the trail says so\n")
	}
	return b.String()
}

// Run checks the clock, records the reading, and reports it.
//
// An offset outside the tolerance is an error, so that a scheduled run fails
// where a deployment can see it. The reading is recorded first either way: an
// hour whose timestamps are suspect is exactly the hour an auditor will want
// the measurement from.
func (c ClockSync) Run(ctx context.Context) (ClockReport, error) {
	out := c.Out
	if out == nil {
		out = os.Stdout
	}
	if len(c.Servers) == 0 {
		return ClockReport{}, errors.New("clock-sync: name at least one time reference")
	}

	reading, failures, err := clock.Best(ctx, c.Servers, c.Timeout)
	if err != nil {
		return ClockReport{}, err
	}
	report := ClockReport{
		Source:   reading.Source,
		OffsetMS: reading.Offset.Milliseconds(),
		DelayMS:  reading.Delay.Milliseconds(),
		Stratum:  reading.Stratum,
		Within:   c.MaxOffset <= 0 || abs(reading.Offset) <= c.MaxOffset,
	}
	for _, f := range failures {
		report.Unreachable = append(report.Unreachable, f.Error())
	}

	if err := c.emit(ctx, reading); err != nil {
		printf(out, "%s", report.String())
		return report, err
	}
	report.Recorded = c.Sink != nil

	if c.JSON {
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return report, err
		}
		printf(out, "%s\n", body)
	} else {
		printf(out, "%s", report.String())
	}

	if !report.Within {
		return report, fmt.Errorf(
			"clock-sync: the clock is %d ms from %s, which is more than the %s this deployment allows",
			report.OffsetMS, reading.Source, c.MaxOffset)
	}
	return report, nil
}

// emit records the reading as an audit event, through the same emitter and the
// same catalogue as anything else. The job's account of itself is a record like
// any other, or it would be a log line nobody could verify.
func (c ClockSync) emit(ctx context.Context, reading clock.Reading) error {
	if c.Sink == nil {
		return nil
	}
	if c.Catalogue == nil {
		return errors.New("clock-sync: a catalogue is required to record the reading")
	}
	data, err := structpb.NewStruct(map[string]any{
		// The schema takes an integer: a duration in milliseconds says as much
		// as this measurement can, and the record carries no floating point.
		"offset_ms": float64(reading.Offset.Milliseconds()),
		"source":    reading.Source,
	})
	if err != nil {
		return fmt.Errorf("clock-sync: %w", err)
	}

	emitter, err := emit.New(emit.Options{
		Source:    c.Catalogue.Source,
		Catalogue: c.Catalogue,
		Sink:      c.Sink,
		// This is the job's account of itself, so it is delivered async
		// whatever the catalogue declares. See emit.Options.SelfReporting.
		SelfReporting: true,
		Version:       c.Version,
		Instance:      c.Instance,
	})
	if err != nil {
		return fmt.Errorf("clock-sync: %w", err)
	}
	defer emitter.Close() //nolint:errcheck // the record is already written or dropped

	return emitter.Record(ctx, &record.Record{
		Action:    "audit.clock.synchronised",
		Operation: auditv1.Operation_OPERATION_ACCESS,
		TenantId:  record.TenantPlatform,
		// A record with no actor at all cannot be told from one whose actor was
		// stripped, so the job names itself as the machine identity it is.
		Actor: &record.Actor{Kind: "system", Id: c.Instance},
		Data:  data,
	})
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
