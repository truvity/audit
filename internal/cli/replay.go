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

	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store"
)

// DeadLetterPrefix is where the writer keeps what it could not process.
const DeadLetterPrefix = "dlq/"

// Replay sends dead letters back to a writer.
//
// Nothing the writer cannot process is dropped: it is kept under the
// dead-letter prefix with the reason, so that a fault upstream is a thing an
// operator fixes rather than a gap nobody can account for. Replay is the second
// half of that promise. Once the cause is fixed — a catalogue version
// registered, a profile configured, an emitter corrected — the records go back
// in, keeping their identifiers, and the writer's deduplication makes a replay
// of something that did get through cost nothing.
//
// What replay does not do is repair a record. A record that was refused because
// it does not satisfy its schema will be refused again, and should be: the
// archive is not the place to correct what an emitter got wrong.
type Replay struct {
	Store store.Store
	// Sink is the writer. Nil means a dry run: the dead letters are read and
	// summarised and nothing is sent.
	Sink     sink.Sink
	From, To time.Time
	// Reason keeps only dead letters whose reason contains this text, which is
	// how an operator replays the cause they fixed and not the others.
	Reason string
	// Action keeps only dead letters of one action.
	Action string
	// Batch is how many records are sent at a time. Default 100.
	Batch int
	Out   io.Writer
}

// ReplayReport is what a replay found and did.
type ReplayReport struct {
	Read     int            `json:"read"`
	Sent     int            `json:"sent"`
	Skipped  int            `json:"skipped"`
	DeadEnd  int            `json:"dead_end"`
	Reasons  map[string]int `json:"reasons"`
	Rejected []string       `json:"rejected,omitempty"`
}

// Run reads the dead letters of the range and, unless this is a dry run, sends
// them back.
//
// It reports how many failed again by listing the dead-letter prefix before and
// after. That works because a writer dead-letters within the call that carried
// the record, so once a blocking write returns, whatever it could not process
// is already back under the prefix. It is also the only way to tell: a writer
// accepts a record it dead-letters, and it is right to, because a record that
// can never be valid must not be retried forever by everything below.
func (r Replay) Run(ctx context.Context) (ReplayReport, error) {
	out := r.Out
	if out == nil {
		out = os.Stdout
	}
	report := ReplayReport{Reasons: map[string]int{}}

	before, err := r.deadLetters(ctx)
	if err != nil {
		return report, err
	}
	known := make(map[string]bool, len(before))
	for _, key := range before {
		known[key] = true
	}

	var batch []*record.Record
	send := func() error {
		if len(batch) == 0 {
			return nil
		}
		res, err := r.Sink.Write(ctx, &sink.Request{Records: batch, Delivery: sink.Block})
		if err != nil {
			return fmt.Errorf("replay: the writer refused a batch of %d: %w", len(batch), err)
		}
		for _, x := range res.Rejected {
			report.Rejected = append(report.Rejected, x.ID+": "+x.Reason)
		}
		report.Sent += res.Accepted
		batch = batch[:0]
		return nil
	}

	for _, key := range before {
		body, err := r.Store.Get(ctx, key)
		if err != nil {
			return report, fmt.Errorf("replay: %s: %w", key, err)
		}
		var envelope deadLetter
		if err := json.Unmarshal(body, &envelope); err != nil {
			return report, fmt.Errorf("replay: %s: %w", key, err)
		}
		report.Read++
		report.Reasons[summarise(envelope.Reason)]++

		if !r.keeps(envelope) {
			report.Skipped++
			continue
		}
		if r.Sink == nil {
			continue
		}
		var rec record.Record
		if err := record.Unmarshal(envelope.Record, &rec); err != nil {
			// A dead letter whose record cannot be decoded is one the writer
			// could not encode either. It stays where it is; nothing else can
			// be done with it here.
			report.DeadEnd++
			continue
		}
		batch = append(batch, &rec)
		if len(batch) >= r.batchSize() {
			if err := send(); err != nil {
				return report, err
			}
		}
	}
	if r.Sink != nil {
		if err := send(); err != nil {
			return report, err
		}
		after, err := r.deadLetters(ctx)
		if err != nil {
			return report, err
		}
		for _, key := range after {
			if !known[key] {
				report.DeadEnd++
			}
		}
	}
	r.print(out, report)
	return report, nil
}

// deadLetters lists the range's dead letters, day by day, because the prefix is
// partitioned by day and a whole-prefix listing of a long-lived archive is not
// something a command should do to find a week.
func (r Replay) deadLetters(ctx context.Context) ([]string, error) {
	var keys []string
	for day := r.From.UTC().Truncate(24 * time.Hour); !day.After(r.To.UTC()); day = day.AddDate(0, 0, 1) {
		prefix := fmt.Sprintf("%syear=%s/month=%s/day=%s/",
			DeadLetterPrefix, day.Format("2006"), day.Format("01"), day.Format("02"))
		entries, err := r.Store.List(ctx, prefix, "", 0)
		if err != nil {
			return nil, fmt.Errorf("replay: listing %s: %w", prefix, err)
		}
		for _, e := range entries {
			keys = append(keys, e.Key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// deadLetter is the envelope the writer writes, read back.
type deadLetter struct {
	Reason string          `json:"reason"`
	At     string          `json:"at"`
	Writer string          `json:"writer"`
	ID     string          `json:"id"`
	Source string          `json:"source"`
	Action string          `json:"action"`
	Record json.RawMessage `json:"record"`
}

func (r Replay) keeps(d deadLetter) bool {
	if r.Reason != "" && !strings.Contains(d.Reason, r.Reason) {
		return false
	}
	if r.Action != "" && d.Action != r.Action {
		return false
	}
	return true
}

func (r Replay) batchSize() int {
	if r.Batch > 0 {
		return r.Batch
	}
	return 100
}

func (r Replay) print(out io.Writer, report ReplayReport) {
	if r.Sink == nil {
		printf(out, "dry run: nothing was sent\n\n")
	}
	printf(out, "read      %d\n", report.Read)
	if r.Sink != nil {
		printf(out, "sent      %d\n", report.Sent)
	}
	if report.Skipped > 0 {
		printf(out, "skipped   %d (filtered out)\n", report.Skipped)
	}
	if report.DeadEnd > 0 {
		printf(out, "failed    %d (still under %s)\n", report.DeadEnd, DeadLetterPrefix)
	}
	if len(report.Reasons) > 0 {
		printf(out, "\nreasons:\n")
		for _, reason := range sortedByCount(report.Reasons) {
			printf(out, "  %5d  %s\n", report.Reasons[reason], reason)
		}
	}
	for _, x := range report.Rejected {
		printf(out, "rejected  %s\n", x)
	}
}

// summarise keeps a reason short enough to group by, since most of them are one
// fault repeated and the operator wants the shape, not every instance.
func summarise(reason string) string {
	reason = strings.TrimSpace(strings.SplitN(reason, "\n", 2)[0])
	if len(reason) > 120 {
		return reason[:117] + "..."
	}
	return reason
}

func sortedByCount(counts map[string]int) []string {
	out := make([]string, 0, len(counts))
	for k := range counts {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if counts[out[i]] != counts[out[j]] {
			return counts[out[i]] > counts[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}
