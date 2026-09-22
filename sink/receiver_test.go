package sink_test

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// The receiver stamps with the caller the authenticator verified, and the
// application cannot choose it. Everything downstream of a stream reads
// messages rather than serving a request, so an identity not attached here is
// an identity nothing can recover.
func TestReceiverStampsTheVerifiedCaller(t *testing.T) {
	next := &sink.Memory{}
	at := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	r := &sink.Receiver{To: next, Version: "1.2.3", Instance: "receiver-1", Now: func() time.Time { return at }}

	rec := &record.Record{
		Id: record.NewID(), Source: "shop", Action: "shop.order.viewed",
		// What an application might like to be recorded as.
		Observer: &record.Observer{Id: "somebody-else"},
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Subject: "system:serviceaccount:shop:api"})
	if _, err := r.Write(ctx, &sink.Request{Records: []*record.Record{rec}}); err != nil {
		t.Fatal(err)
	}

	if got := rec.GetObserver().GetId(); got != "system:serviceaccount:shop:api" {
		t.Errorf("observer = %q, want the verified caller and not the record's own claim", got)
	}
	if got := rec.GetObserver().GetVersion(); got != "1.2.3" {
		t.Errorf("version = %q, want the receiver's", got)
	}
	if got := rec.GetRecordedAt().AsTime(); !got.Equal(at) {
		t.Errorf("recorded_at = %s, want the moment the trail took responsibility (%s)", got, at)
	}
	if rec.GetOriginHash() == "" {
		t.Error("no origin hash: nothing downstream could tell this stamp from an edited one")
	}
	if next.Len() != 1 {
		t.Errorf("the batch was not forwarded: %d records", next.Len())
	}
}

// A caller nobody verified is recorded as nobody, or as the receiver itself
// where it was told to name one. It is never recorded as whoever the record
// asked to be.
func TestReceiverDoesNotTakeTheRecordsWordForIt(t *testing.T) {
	next := &sink.Memory{}
	r := &sink.Receiver{To: next}
	rec := &record.Record{
		Id: record.NewID(), Source: "shop", Action: "shop.order.viewed",
		Observer: &record.Observer{Id: "system:serviceaccount:kube-system:somebody"},
	}
	if _, err := r.Write(context.Background(), &sink.Request{Records: []*record.Record{rec}}); err != nil {
		t.Fatal(err)
	}
	if got := rec.GetObserver().GetId(); got != "" {
		t.Errorf("observer = %q, want nobody: no caller was verified", got)
	}
}
