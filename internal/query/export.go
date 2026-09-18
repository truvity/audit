package query

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/truvity/audit/auth"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/index"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/store"
)

// ExportPrefix is where exports live.
//
// Outside every profile's prefix, deliberately. An export is a copy of records
// made to be taken away, and it must not be mistaken for the archive: it is not
// under an object lock, no digest accounts for it, and it expires. A reader who
// found one under a profile prefix would have no way to tell it from a record.
const ExportPrefix = "export"

// Exporter writes exports and hands out links to them.
type Exporter struct {
	// Store is where exports are written. It may be the archive's bucket under
	// the export prefix, or a bucket of its own; it must not be a prefix any
	// profile writes to.
	Store store.Store
	// Presigner makes the link. Without one an export can be produced and not
	// collected, so the service refuses rather than filling a bucket with
	// files nobody can reach.
	Presigner store.Presigner
	// Expiry is how long an export is kept. It has a short default and no
	// unlimited setting: an export is an unlocked copy of audit records, and
	// one that outlives the question it answered is a second archive that
	// nobody is managing. Default 7 days.
	Expiry time.Duration
	// LinkValid is how long a link works. Default one hour: a URL to audit
	// records that outlives the conversation it was shared in is a copy of the
	// trail nobody is tracking.
	LinkValid time.Duration
	Now       func() time.Time
}

// Job is what was asked for and what became of it.
type Job struct {
	ID        string     `json:"id"`
	Profile   string     `json:"profile"`
	Format    string     `json:"format"`
	By        string     `json:"by"`
	Rule      string     `json:"rule"`
	AskedAt   time.Time  `json:"asked_at"`
	Records   int        `json:"records,omitempty"`
	DoneAt    *time.Time `json:"done_at,omitempty"`
	Failed    string     `json:"failed,omitempty"`
	ExpiresAt time.Time  `json:"expires_at"`
}

// State is where a job has got to.
func (j Job) State() auditv1.ExportState {
	switch {
	case j.Failed != "":
		return auditv1.ExportState_EXPORT_STATE_ERROR
	case j.DoneAt != nil:
		return auditv1.ExportState_EXPORT_STATE_READY
	default:
		return auditv1.ExportState_EXPORT_STATE_PENDING
	}
}

// Keys of a job's two records and its file. Append-only, like every other
// record here: nothing in this archive is overwritten, so what a job is now is
// read from which of the two exist.
func askedKey(id string) string { return ExportPrefix + "/" + id + "/asked.json" }
func doneKey(id string) string  { return ExportPrefix + "/" + id + "/done.json" }

// FileKey is where the export itself is written.
func FileKey(id, format string) string {
	return ExportPrefix + "/" + id + "/records." + format
}

// Export runs an export and records it.
//
// It runs to completion before returning, which is the honest shape for what
// this can currently promise: a job handed to a goroutine would be lost by a
// restart, and a caller polling for a job no replica remembers is worse than
// waiting. The state machine in the contract is kept because an implementation
// that does defer the work will need it, and because the caller's code should
// not change when it does.
func (s *Service) Export(
	ctx context.Context, p auth.Principal, req *auditv1.ExportRequest,
) (Job, error) {
	if s.Exporter == nil || s.Exporter.Store == nil {
		return Job{}, errors.New("query: no exporter is configured")
	}
	if s.Exporter.Presigner == nil {
		return Job{}, errors.New(
			"query: no presigner is configured, so an export could be produced and never collected")
	}
	g, err := s.allow(ctx, p, req.GetProfile(), auth.Export)
	if err != nil {
		s.record(ctx, "audit.export.requested", p, g, err, []*record.Target{
			{Type: "profile", Id: req.GetProfile()},
		})
		return Job{}, err
	}

	format := "ndjson"
	if req.GetFormat() == auditv1.ExportRequest_FORMAT_CSV {
		format = "csv"
	}
	job := Job{
		ID: record.NewID(), Profile: req.GetProfile(), Format: format,
		By: p.Subject, Rule: g.Rule, AskedAt: s.Exporter.now(),
	}
	job.ExpiresAt = job.AskedAt.Add(s.Exporter.expiry())

	// The request is recorded before anything is read, and block delivery is
	// what the catalogue declares for it: an export is the one read that leaves
	// with the records, so if the trail cannot say it was asked for, it does
	// not happen.
	if err := s.recordExportRequest(ctx, p, g, job); err != nil {
		return Job{}, err
	}
	if err := s.Exporter.write(ctx, job, askedKey(job.ID)); err != nil {
		return Job{}, err
	}

	rows, err := s.collect(ctx, req, g)
	if err != nil {
		job.Failed = err.Error()
	} else {
		body, err := render(rows, format)
		if err != nil {
			job.Failed = err.Error()
		} else if err := s.Exporter.Store.Put(ctx, store.Object{
			Key: FileKey(job.ID, format), Body: body,
			RetainUntil: job.ExpiresAt, ContentType: contentType(format),
		}); err != nil {
			job.Failed = err.Error()
		} else {
			job.Records = len(rows)
		}
	}
	done := s.Exporter.now()
	job.DoneAt = &done
	if err := s.Exporter.write(ctx, job, doneKey(job.ID)); err != nil {
		return job, err
	}

	s.recordExportCompleted(ctx, p, g, job)
	return job, nil
}

// GetExport returns a job and, when it is ready, a link to it.
func (s *Service) GetExport(ctx context.Context, p auth.Principal, id string) (Job, string, error) {
	if s.Exporter == nil || s.Exporter.Store == nil {
		return Job{}, "", errors.New("query: no exporter is configured")
	}
	job, err := s.Exporter.read(ctx, id)
	if err != nil {
		return Job{}, "", err
	}
	// Whoever asked for it is whoever may collect it. An export is a copy of
	// records that has left the service's control, so the link is not something
	// to hand to a second person on the strength of a job identifier.
	if job.By != p.Subject {
		return Job{}, "", fmt.Errorf("query: no export %s", id)
	}
	if job.State() != auditv1.ExportState_EXPORT_STATE_READY {
		return job, "", nil
	}
	if !s.Exporter.now().Before(job.ExpiresAt) {
		return job, "", fmt.Errorf("query: export %s expired at %s",
			id, job.ExpiresAt.Format(time.RFC3339))
	}
	url, err := s.Exporter.Presigner.Presign(ctx,
		FileKey(job.ID, job.Format), s.Exporter.linkValid())
	if err != nil {
		return job, "", err
	}
	return job, url, nil
}

// collect reads every row the export covers, bounded by the grant.
func (s *Service) collect(
	ctx context.Context, req *auditv1.ExportRequest, g auth.Grant,
) ([]index.Row, error) {
	compiled, err := Compile(&auditv1.SearchRequest{
		Profile: req.GetProfile(), Filter: req.GetFilter(),
	})
	if err != nil {
		return nil, err
	}
	q := s.narrow(compiled, g)
	q.Limit = 1000

	var out []index.Row
	for {
		page, err := s.Searcher.Search(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Rows...)
		if !page.More || page.Next == nil {
			return out, nil
		}
		q.After = page.Next
	}
}

func (s *Service) recordExportRequest(
	ctx context.Context, p auth.Principal, g auth.Grant, job Job,
) error {
	s.record(ctx, "audit.export.requested", p, g, nil, append([]*record.Target{
		{Type: "export", Id: job.ID},
		{Type: "profile", Id: job.Profile},
	}, tenantTargets(g)...))
	return nil
}

func (s *Service) recordExportCompleted(
	ctx context.Context, p auth.Principal, g auth.Grant, job Job,
) {
	if s.emitter == nil {
		return
	}
	data, err := structData(map[string]any{
		"records": float64(job.Records), "format": job.Format,
	})
	if err != nil {
		s.unrecorded("audit.export.completed", err)
		return
	}
	var failure error
	if job.Failed != "" {
		failure = errors.New(job.Failed)
	}
	s.recordWithData(ctx, "audit.export.completed", p, g, failure, []*record.Target{
		{Type: "export", Id: job.ID},
	}, data)
}

// write puts one of a job's records.
func (e *Exporter) write(ctx context.Context, job Job, key string) error {
	body, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	if err := e.Store.Put(ctx, store.Object{
		Key: key, Body: body, RetainUntil: job.ExpiresAt,
		ContentType: "application/json",
	}); err != nil {
		return fmt.Errorf("query: recording export %s: %w", job.ID, err)
	}
	return nil
}

// read returns a job as it now stands.
func (e *Exporter) read(ctx context.Context, id string) (Job, error) {
	for _, key := range []string{doneKey(id), askedKey(id)} {
		body, err := e.Store.Get(ctx, key)
		if err != nil {
			continue
		}
		var job Job
		if err := json.Unmarshal(body, &job); err != nil {
			return Job{}, fmt.Errorf("query: export %s: %w", id, err)
		}
		return job, nil
	}
	return Job{}, fmt.Errorf("query: no export %s", id)
}

func (e *Exporter) expiry() time.Duration {
	if e.Expiry > 0 {
		return e.Expiry
	}
	return 7 * 24 * time.Hour
}

func (e *Exporter) linkValid() time.Duration {
	if e.LinkValid > 0 {
		return e.LinkValid
	}
	return time.Hour
}

func (e *Exporter) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func contentType(format string) string {
	if format == "csv" {
		return "text/csv"
	}
	return "application/x-ndjson"
}

// render writes the rows in the form asked for.
func render(rows []index.Row, format string) ([]byte, error) {
	var b bytes.Buffer
	if format == "csv" {
		w := csv.NewWriter(&b)
		header := []string{
			"id", "tenant_id", "occurred_at", "recorded_at", "source", "action",
			"operation", "outcome", "actor_kind", "actor_id", "object_key", "line",
		}
		if err := w.Write(header); err != nil {
			return nil, err
		}
		for _, r := range rows {
			if err := w.Write([]string{
				r.ID, r.TenantID, r.OccurredAt.UTC().Format(time.RFC3339Nano),
				r.RecordedAt.UTC().Format(time.RFC3339Nano), r.Source, r.Action,
				r.Operation, r.Outcome, r.ActorKind, r.ActorID,
				r.ObjectKey, strconv.Itoa(r.Line),
			}); err != nil {
				return nil, err
			}
		}
		w.Flush()
		return b.Bytes(), w.Error()
	}

	for _, r := range rows {
		line, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}
