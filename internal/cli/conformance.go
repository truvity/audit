package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/gen/audit/v1/auditv1connect"
)

// Conformance holds a running query service to what the search contract
// promises, using nothing but the API a caller has.
//
// It reads and never writes. A conformance run that wrote test records into a
// deployment would leave them in an Object-Locked archive for as long as the
// profile keeps anything — years, for evidence — so it checks the promises over
// whatever the deployment already holds. The reads it makes are recorded by
// the service like any other; that is the service doing its job.
//
// What it checks, per profile: paging walks every record once, newest first;
// the last page still has a `next`; every record is the profile's copy; Get
// returns what Search returned, with its provenance; a filter returns only
// what matches it and the record it was built from; a cursor is refused with
// another query; an unknown id is not found. With VerifiedBefore, a record
// older than that must be covered by a verified digest.
type Conformance struct {
	Client   auditv1connect.QueryServiceClient
	Profiles []string
	// Max bounds how many records a profile's walk reads. Default 1000.
	Max int
	// PageSize is the limit each page asks for. Default 100.
	PageSize int32
	// VerifiedBefore, when set, requires every sampled record that occurred
	// longer ago than this to be covered by a verified digest: the digest and
	// verify jobs have run and found it intact.
	VerifiedBefore time.Duration
	Now            func() time.Time
	JSON           bool
	Out            io.Writer
}

// ConformanceFinding is one check's result.
type ConformanceFinding struct {
	Profile string `json:"profile"`
	Check   string `json:"check"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
}

// Run reports the number of checks that failed. An error is a run that could
// not be made at all — the service unreachable, the caller refused.
func (c Conformance) Run(ctx context.Context) (int, error) {
	out := c.Out
	if out == nil {
		out = os.Stdout
	}
	var findings []ConformanceFinding
	for _, profile := range c.Profiles {
		got, err := c.profile(ctx, profile)
		if err != nil {
			return 0, fmt.Errorf("conformance: %s: %w", profile, err)
		}
		findings = append(findings, got...)
	}
	failed := 0
	for _, f := range findings {
		if !f.OK {
			failed++
		}
	}
	if c.JSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return failed, enc.Encode(findings)
	}
	for _, f := range findings {
		state := "valid  "
		if !f.OK {
			state = "INVALID"
		}
		line := fmt.Sprintf("%s  %s  %s", state, f.Profile, f.Check)
		if f.Detail != "" {
			line += ": " + f.Detail
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return failed, err
		}
	}
	_, err := fmt.Fprintf(out, "%d checks, %d failed\n", len(findings), failed)
	return failed, err
}

// report collects one profile's findings.
type report struct {
	profile  string
	findings []ConformanceFinding
}

// check records a check's result; the detail says what went wrong, and is
// kept only when something did.
func (r *report) check(name string, ok bool, detail string, args ...any) {
	f := ConformanceFinding{Profile: r.profile, Check: name, OK: ok}
	if !ok {
		f.Detail = fmt.Sprintf(detail, args...)
	}
	r.findings = append(r.findings, f)
}

// note records something that passed but is worth saying.
func (r *report) note(name, detail string) {
	r.findings = append(r.findings, ConformanceFinding{Profile: r.profile, Check: name, OK: true, Detail: detail})
}

func (c Conformance) profile(ctx context.Context, profile string) ([]ConformanceFinding, error) {
	limit := c.Max
	if limit <= 0 {
		limit = 1000
	}
	size := c.PageSize
	if size <= 0 {
		size = 100
	}
	r := &report{profile: profile}

	// The walk: every page of the default order until the service runs out or
	// the bound is reached. The first call's error is the run's: a caller the
	// service refuses has nothing to check.
	var (
		records  []*auditv1.Record
		cursor   string
		lastNext string
		pages    int
	)
	for len(records) < limit {
		res, err := c.Client.Search(ctx, connect.NewRequest(&auditv1.SearchRequest{
			Profile: profile, Limit: size, Cursor: cursor,
		}))
		if err != nil {
			if pages == 0 {
				return nil, err
			}
			r.check("paging", false, "page %d refused: %v", pages+1, err)
			break
		}
		pages++
		records = append(records, res.Msg.GetItems()...)
		lastNext = res.Msg.GetCursors().GetNext()
		if len(res.Msg.GetItems()) == 0 || lastNext == "" {
			break
		}
		cursor = lastNext
	}
	if len(records) > limit {
		records = records[:limit]
	}

	c.walked(r, profile, records, pages, lastNext)
	if len(records) == 0 {
		r.note("records", "the profile holds nothing this caller may read; the remaining checks need a record")
		return r.findings, nil
	}
	c.gets(ctx, r, profile, records)
	c.filters(ctx, r, profile, records)
	c.refusals(ctx, r, profile, size)
	return r.findings, nil
}

// walked checks what the walk itself shows.
func (c Conformance) walked(r *report, profile string, records []*auditv1.Record, pages int, lastNext string) {
	seen := map[string]bool{}
	dup, order, foreign := "", "", ""
	for i, rec := range records {
		if seen[rec.GetId()] && dup == "" {
			dup = rec.GetId()
		}
		seen[rec.GetId()] = true
		if p := rec.GetProfile(); p != "" && p != profile && foreign == "" {
			foreign = fmt.Sprintf("%s is a %s copy", rec.GetId(), p)
		}
		if i > 0 && order == "" && before(records[i-1], rec) {
			order = fmt.Sprintf("%s (%s) came before %s (%s)",
				records[i-1].GetId(), stamp(records[i-1]), rec.GetId(), stamp(rec))
		}
	}
	r.check("paging returns each record once", dup == "", "%s came back twice", dup)
	r.check("newest first, then by id", order == "", "%s", order)
	r.check("every record is this profile's copy", foreign == "", "%s", foreign)
	if pages > 0 {
		r.check("the last page still has next", lastNext != "",
			"a page ended the walk with no next cursor; the contract keeps next so the same call is the tail")
	}
}

// before reports whether a comes before b wrongly in newest-first order: a is
// older, or equally old with a larger id.
func before(a, b *auditv1.Record) bool {
	ta, tb := a.GetOccurredAt().AsTime(), b.GetOccurredAt().AsTime()
	if ta.Equal(tb) {
		return a.GetId() > b.GetId()
	}
	return ta.Before(tb)
}

func stamp(rec *auditv1.Record) string {
	return rec.GetOccurredAt().AsTime().UTC().Format(time.RFC3339Nano)
}

// samples are the records the per-record checks use: the newest, the oldest
// read, and one between.
func samples(records []*auditv1.Record) []*auditv1.Record {
	out := []*auditv1.Record{records[0]}
	if len(records) > 2 {
		out = append(out, records[len(records)/2])
	}
	if len(records) > 1 {
		out = append(out, records[len(records)-1])
	}
	return out
}

func (c Conformance) gets(ctx context.Context, r *report, profile string, records []*auditv1.Record) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	for _, rec := range samples(records) {
		res, err := c.Client.Get(ctx, connect.NewRequest(&auditv1.GetRequest{Profile: profile, Id: rec.GetId()}))
		if err != nil {
			r.check("get returns what search returned", false, "%s: %v", rec.GetId(), err)
			continue
		}
		r.check("get returns what search returned", proto.Equal(res.Msg.GetRecord(), rec),
			"%s differs between Search and Get", rec.GetId())
		prov := res.Msg.GetProvenance()
		r.check("get says where the copy is", prov.GetObjectKey() != "", "%s has no object key", rec.GetId())
		if c.VerifiedBefore > 0 && rec.GetOccurredAt().AsTime().Before(now().Add(-c.VerifiedBefore)) {
			r.check("old records are covered by a verified digest", prov.GetVerifiedAt() != nil,
				"%s occurred %s and no verified digest covers it", rec.GetId(), stamp(rec))
		}
	}
}

func (c Conformance) filters(ctx context.Context, r *report, profile string, records []*auditv1.Record) {
	sample := records[len(records)/2]
	search := func(f *auditv1.Filter) ([]*auditv1.Record, error) {
		res, err := c.Client.Search(ctx, connect.NewRequest(&auditv1.SearchRequest{
			Profile: profile, Filter: []*auditv1.Filter{f}, Limit: 100,
		}))
		if err != nil {
			return nil, err
		}
		return res.Msg.GetItems(), nil
	}
	equal := func(v string) *auditv1.StringPredicate {
		return &auditv1.StringPredicate{Operator: &auditv1.StringPredicate_Equal{Equal: v}}
	}

	byID, err := search(&auditv1.Filter{Id: equal(sample.GetId())})
	r.check("a filter on id returns that record alone",
		err == nil && len(byID) == 1 && proto.Equal(byID[0], sample), "%s", describe(err, byID, sample))

	byAction, err := search(&auditv1.Filter{Action: equal(sample.GetAction())})
	r.check("a filter on action returns only that action",
		err == nil && all(byAction, func(x *auditv1.Record) bool { return x.GetAction() == sample.GetAction() }),
		"%s", describe(err, byAction, nil))

	byTenant, err := search(&auditv1.Filter{TenantId: equal(sample.GetTenantId())})
	r.check("a filter on tenant returns only that tenant",
		err == nil && all(byTenant, func(x *auditv1.Record) bool { return x.GetTenantId() == sample.GetTenantId() }),
		"%s", describe(err, byTenant, nil))

	at := sample.GetOccurredAt().AsTime()
	window := &auditv1.TimePredicate{Operator: &auditv1.TimePredicate_Between{Between: &auditv1.TimeRange{
		From: timestamppb.New(at), To: timestamppb.New(at.Add(time.Second)),
	}}}
	byTime, err := search(&auditv1.Filter{OccurredAt: window})
	inWindow := func(x *auditv1.Record) bool {
		t := x.GetOccurredAt().AsTime()
		return !t.Before(at) && t.Before(at.Add(time.Second))
	}
	r.check("a time range returns only what occurred in it, and includes the record it was built from",
		err == nil && all(byTime, inWindow) && contains(byTime, sample.GetId()), "%s", describe(err, byTime, sample))
}

func (c Conformance) refusals(ctx context.Context, r *report, profile string, size int32) {
	first, err := c.Client.Search(ctx, connect.NewRequest(&auditv1.SearchRequest{Profile: profile, Limit: size}))
	if err == nil {
		next := first.Msg.GetCursors().GetNext()
		_, err = c.Client.Search(ctx, connect.NewRequest(&auditv1.SearchRequest{
			Profile: profile, Limit: size, Cursor: next,
			Filter: []*auditv1.Filter{{Action: &auditv1.StringPredicate{
				Operator: &auditv1.StringPredicate_Equal{Equal: "conformance.no-such-action"},
			}}},
		}))
		r.check("a cursor is refused with another query", connect.CodeOf(err) == connect.CodeInvalidArgument,
			"the service answered %v", codeOrOK(err))
	}
	_, err = c.Client.Get(ctx, connect.NewRequest(&auditv1.GetRequest{Profile: profile, Id: "conformance-no-such-record"}))
	r.check("an unknown id is not found", connect.CodeOf(err) == connect.CodeNotFound, "the service answered %v", codeOrOK(err))
}

func describe(err error, got []*auditv1.Record, want *auditv1.Record) string {
	switch {
	case err != nil:
		return err.Error()
	case want != nil && !contains(got, want.GetId()):
		return fmt.Sprintf("%d records, without %s", len(got), want.GetId())
	default:
		return fmt.Sprintf("%d records, not all matching", len(got))
	}
}

func all(records []*auditv1.Record, ok func(*auditv1.Record) bool) bool {
	for _, x := range records {
		if !ok(x) {
			return false
		}
	}
	return true
}

func contains(records []*auditv1.Record, id string) bool {
	for _, x := range records {
		if x.GetId() == id {
			return true
		}
	}
	return false
}

func codeOrOK(err error) string {
	if err == nil {
		return "ok"
	}
	return connect.CodeOf(err).String()
}
