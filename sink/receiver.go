package sink

import (
	"context"
	"errors"
	"time"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/record"
)

// Receiver is the front door of an installation that keeps its records on a
// stream: it stamps each record with the caller it verified and hands the batch
// to the next hop, which publishes it.
//
// Stamping has to happen here. The consumer on the other side of a stream has
// no caller to verify — it is reading messages, not serving a request — so a
// record that reached the stream unstamped would be written with no observer at
// all, and the trail would not say who reported what. The writer on the far
// side keeps a stamp that verifies rather than replacing it, because it can
// only be reading its own installation's stream.
//
// It is the same reasoning as everywhere else here: a reporter never says who
// it is. The identity comes from the token the authenticator checked, and the
// application cannot choose it.
type Receiver struct {
	// To is the next hop, normally a stream publisher.
	To Sink
	// Self names this receiver when a record arrives with no verified caller,
	// which is an in-process call or a trial install accepting anonymous
	// writes. Empty leaves the observer empty, which is what an anonymous
	// write means.
	Self string
	// Version and Instance name this build and this pod on the records it
	// stamps, where the emitter left them empty.
	Version  string
	Instance string
	// Now is the clock, for tests. Default time.Now.
	Now func() time.Time
}

// Write stamps and forwards.
func (r *Receiver) Write(ctx context.Context, req *Request) (*Result, error) {
	if r.To == nil {
		return nil, errors.New("sink: a receiver needs somewhere to send")
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	at := now().UTC()
	who := auth.SubjectFrom(ctx)
	if who == "" {
		who = r.Self
	}
	for _, rec := range req.Records {
		observer := &record.Observer{
			Id:       who,
			Version:  or(rec.GetObserver().GetVersion(), r.Version),
			Instance: or(rec.GetObserver().GetInstance(), r.Instance),
		}
		// Stamp fills recorded_at and the origin hash over the canonical form,
		// which is what lets the writer tell a stamp it should keep from a
		// record somebody edited on the way.
		if err := record.Stamp(rec, observer, at); err != nil {
			return nil, err
		}
	}
	return r.To.Write(ctx, req)
}

func or(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
