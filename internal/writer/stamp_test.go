package writer_test

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// stampedByReceiver puts a record through a receiver, as one would arrive on a
// stream: the caller verified, the observer stamped, the origin hash over it.
func stampedByReceiver(t *testing.T, at time.Time, subject string) *record.Record {
	t.Helper()
	r := issued(t)
	rc := &sink.Receiver{
		To:  sink.Func(func(context.Context, *sink.Request) (*sink.Result, error) { return &sink.Result{}, nil }),
		Now: func() time.Time { return at },
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Subject: subject})
	if _, err := rc.Write(ctx, &sink.Request{Records: []*record.Record{r}}); err != nil {
		t.Fatal(err)
	}
	return r
}

// A record that reached the archive over a stream keeps the stamp its receiver
// made. The consumer has no caller to verify — it is reading messages, not
// serving a request — so re-stamping would replace a verified identity with
// nothing, and move recorded_at to whenever the backlog happened to be read.
func TestTheWriterKeepsAStampFromItsOwnStream(t *testing.T) {
	b := buildWith(t, parts{
		fromStream: true,
		// A consumer's context carries no principal.
		identity: func(context.Context) string { return "" },
	})
	at := fixedDay(t).Add(-2 * time.Hour)
	r := stampedByReceiver(t, at, "system:serviceaccount:wallet:api")

	if _, err := b.writer.Write(context.Background(), &sink.Request{Records: []*record.Record{r}}); err != nil {
		t.Fatal(err)
	}
	copies := decode(t, b.store)
	if len(copies) == 0 {
		t.Fatal("nothing was written")
	}
	if got := copies[0].GetObserver().GetId(); got != "system:serviceaccount:wallet:api" {
		t.Errorf("observer = %q, want the caller the receiver verified", got)
	}
	if got := copies[0].GetRecordedAt().AsTime(); !got.Equal(at) {
		t.Errorf("recorded_at = %s, want when the receiver took it (%s)", got, at)
	}
}

// A stamp that no longer describes its record is not a stamp. The hash is what
// makes keeping one safe, so an edit between the receiver and the archive loses
// the claim and the writer stamps afresh.
func TestTheWriterRestampsARecordThatWasEdited(t *testing.T) {
	b := buildWith(t, parts{
		fromStream: true,
		identity:   func(context.Context) string { return "" },
	})
	r := stampedByReceiver(t, fixedDay(t).Add(-2*time.Hour), "system:serviceaccount:wallet:api")
	r.Observer.Id = "system:serviceaccount:kube-system:somebody"

	if _, err := b.writer.Write(context.Background(), &sink.Request{Records: []*record.Record{r}}); err != nil {
		t.Fatal(err)
	}
	copies := decode(t, b.store)
	if len(copies) == 0 {
		t.Fatal("nothing was written")
	}
	if got := copies[0].GetObserver().GetId(); got == "system:serviceaccount:kube-system:somebody" {
		t.Error("an edited stamp was kept; the hash is what makes keeping one safe")
	}
}

// On the sink's own port there is a caller to verify, so a stamp a caller
// arrived with is replaced whatever it says. Trusting one here would let an
// application choose the identity it is recorded under.
func TestTheWriterAlwaysStampsWhatArrivesOnItsPort(t *testing.T) {
	b := buildWith(t, parts{}) // not from a stream
	r := stampedByReceiver(t, fixedDay(t).Add(-2*time.Hour), "system:serviceaccount:wallet:api")

	if _, err := b.writer.Write(context.Background(), &sink.Request{Records: []*record.Record{r}}); err != nil {
		t.Fatal(err)
	}
	copies := decode(t, b.store)
	if len(copies) == 0 {
		t.Fatal("nothing was written")
	}
	if got := copies[0].GetObserver().GetId(); got != "workload:wallet" {
		t.Errorf("observer = %q, want the caller this writer verified itself", got)
	}
}
