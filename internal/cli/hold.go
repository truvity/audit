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

	"github.com/truvity/audit/catalogue"
	auditv1 "github.com/truvity/audit/gen/audit/v1"
	"github.com/truvity/audit/internal/hold"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
	"github.com/truvity/audit/store"
)

// Hold places, releases and lists legal holds.
//
// Placing one is an operator action and releasing one needs the deployment's
// break-glass role; neither is decided here. What this does is make the call
// and record that it was made, so that a hold and its release are as accounted
// for as the records they cover.
type Hold struct {
	Store     store.Store
	Sink      sink.Sink
	Catalogue *catalogue.Catalogue
	// RetainUntil is how long a hold's own record is locked for.
	RetainUntil func(time.Time) time.Time

	Profile, Tenant, Reason, ID, By string
	Now                             func() time.Time
	JSON                            bool
	Out                             io.Writer
}

// Run dispatches place, release or list.
func (h Hold) Run(ctx context.Context, action string) error {
	out := h.Out
	if out == nil {
		out = os.Stdout
	}
	holds := hold.Store{Store: h.Store, RetainUntil: h.RetainUntil, Now: h.Now}

	switch action {
	case "place":
		return h.place(ctx, out, holds)
	case "release":
		return h.release(ctx, out, holds)
	case "list":
		return h.list(ctx, out, holds)
	default:
		return fmt.Errorf("audit hold: no action %q; place, release or list", action)
	}
}

func (h Hold) place(ctx context.Context, out io.Writer, holds hold.Store) error {
	id := h.ID
	if id == "" {
		// A hold's identifier is how it is released later, so it is worth being
		// readable: the day and the profile, with a unique tail.
		id = fmt.Sprintf("%s-%s-%s", h.now().Format("2006-01-02"), h.Profile, record.NewID()[:8])
	}
	placed, err := holds.Place(ctx, hold.Record{
		ID: id, Profile: h.Profile, Tenant: h.Tenant,
		Reason: h.Reason, PlacedBy: h.by(),
	})
	if err != nil {
		return err
	}
	h.record(ctx, "audit.hold.placed", placed, nil)
	return h.report(out, placed, fmt.Sprintf("held %d object(s)", placed.Objects))
}

func (h Hold) release(ctx context.Context, out io.Writer, holds hold.Store) error {
	if h.ID == "" {
		return errors.New("audit hold release: name the hold with --id")
	}
	released, err := holds.Release(ctx, h.ID, h.by())
	if err != nil {
		// A release that was refused is recorded as an attempt. Whoever holds
		// the break-glass role should not be able to try quietly.
		h.record(ctx, "audit.hold.released", hold.Record{ID: h.ID, Profile: h.Profile}, err)
		return err
	}
	h.record(ctx, "audit.hold.released", released, nil)
	return h.report(out, released, fmt.Sprintf("released %d object(s)", released.Objects))
}

func (h Hold) list(ctx context.Context, out io.Writer, holds hold.Store) error {
	all, err := holds.List(ctx)
	if err != nil {
		return err
	}
	if h.Profile != "" {
		var kept []hold.Record
		for _, r := range all {
			if r.Covers(h.Profile, h.Tenant) {
				kept = append(kept, r)
			}
		}
		all = kept
	}
	if h.JSON {
		body, err := json.MarshalIndent(all, "", "  ")
		if err != nil {
			return err
		}
		printf(out, "%s\n", body)
		return nil
	}
	if len(all) == 0 {
		printf(out, "no holds\n")
		return nil
	}
	for _, r := range all {
		where := "profile=" + r.Profile
		if r.Tenant != "" {
			where += " tenant=" + r.Tenant
		}
		state := "ACTIVE  "
		if !r.Active() {
			state = "released"
		}
		printf(out, "%s %s  %s  placed %s by %s: %s\n",
			state, r.ID, where, r.PlacedAt.Format(time.RFC3339), r.PlacedBy, r.Reason)
	}
	return nil
}

func (h Hold) report(out io.Writer, r hold.Record, what string) error {
	if h.JSON {
		body, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return err
		}
		printf(out, "%s\n", body)
		return nil
	}
	printf(out, "%s %s: %s\n", r.ID, what, r.Reason)
	return nil
}

// record puts the action in the trail, through the same catalogue as anything
// else. A hold changes what may be deleted, so it belongs there more than most.
func (h Hold) record(ctx context.Context, action string, r hold.Record, failure error) {
	reporter, err := newReporter(h.Catalogue, h.Sink, "", record.InstanceName())
	if err != nil || reporter == nil {
		return
	}
	defer reporter.close()

	event := reporter.event(action, "hold", r.ID)
	if failure != nil {
		event = failed(event, auditv1.Operation_OPERATION_MODIFY, failure.Error())
	} else {
		event = succeeded(event, auditv1.Operation_OPERATION_MODIFY)
	}
	reporter.record(ctx, event)
}

// by is who is acting. It is not defaulted: a hold is an operator's action and
// the record has to name the operator, so the library refuses a blank and the
// refusal says what to pass.
func (h Hold) by() string { return strings.TrimSpace(h.By) }

func (h Hold) now() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}
