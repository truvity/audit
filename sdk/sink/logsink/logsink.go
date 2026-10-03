// Package logsink writes each record as one JSON line to a writer, standard
// output by default, and reports Logged.
//
// It exists for development, and for the deployment that has accepted that its
// log pipeline is its record. Logged is the weakest durability there is, and
// `require:` is what keeps this sink from being the accident: a chain that
// ends here never satisfies `queued` or `archived`.
//
// A line is a JSON object whose MESSAGE is a sentence a person can read and
// whose AUDIT_RECORD is the record itself, so a pipeline that parses JSON keeps
// both. MESSAGE is the field journald and most log shippers take as the text of
// an entry.
//
// TODO: speak journald's native protocol to its socket, so that the record's
// fields arrive as journal fields rather than inside one JSON message.
package logsink

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"

	"github.com/truvity/audit/sdk/record"
	"github.com/truvity/audit/sdk/sink"
)

// Options configure a log sink.
type Options struct {
	// Out receives the lines. Default os.Stdout.
	Out io.Writer
	// Message renders a record as a sentence, normally from its catalogue's
	// message templates. Default: the record's action.
	Message func(*record.Record) string
}

// Sink is a sink.Sink that logs.
type Sink struct {
	mu      sync.Mutex
	out     io.Writer
	message func(*record.Record) string
}

// New returns a log sink.
func New(o Options) *Sink {
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Message == nil {
		o.Message = func(r *record.Record) string { return r.GetAction() }
	}
	return &Sink{out: o.Out, message: o.Message}
}

// Guarantees implements sink.Guarantor.
func (s *Sink) Guarantees() sink.Durability { return sink.Logged }

type line struct {
	Message string          `json:"MESSAGE"`
	Record  json.RawMessage `json:"AUDIT_RECORD"`
}

// Write implements sink.Sink. A record that cannot be encoded is rejected; the
// rest of the batch is still logged.
func (s *Sink) Write(ctx context.Context, req *sink.Request) (*sink.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := &sink.Result{Durability: sink.Logged}
	var out []byte
	for _, r := range req.Records {
		body, err := record.Canonical(r)
		if err != nil {
			result.Rejected = append(result.Rejected, sink.Rejection{
				ID: r.GetId(), Reason: "cannot be encoded: " + err.Error(),
			})
			continue
		}
		b, err := json.Marshal(line{Message: s.message(r), Record: body})
		if err != nil {
			result.Rejected = append(result.Rejected, sink.Rejection{
				ID: r.GetId(), Reason: "cannot be encoded: " + err.Error(),
			})
			continue
		}
		out = append(append(out, b...), '\n')
		result.Accepted++
	}
	if len(out) == 0 {
		return result, nil
	}
	// One write for the batch, under a lock, so that lines from concurrent
	// callers never interleave.
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.out.Write(out); err != nil {
		return nil, err
	}
	return result, nil
}
