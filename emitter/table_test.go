package emitter

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
)

// A product tests its own table, its examples and the domains of its values with its own vectors. These tests hold the
// emitter to a closed table of one action (closedTable).

// verifies: REQ-AUDIT-022
func TestTheEmitterRefusesAnActionThatIsNotInTheTable(t *testing.T) {
	tr := &fakeTransport{}
	e, err := New(Config{Product: "example", Component: "key", Scope: "dev", Release: "0.0.0-test", Transport: tr, Table: closedTable(t), Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	for name, a := range map[string]Action{
		"a name that is no row":        {Name: "key.open", ClassUID: 6003, ActivityID: 99, ActivityName: "Open"},
		"another class than the row":   {Name: "key.sign", ClassUID: 3004, ActivityID: 99, ActivityName: "Sign"},
		"another activity":             {Name: "key.sign", ClassUID: 6003, ActivityID: 1},
		"another name of the activity": {Name: "key.sign", ClassUID: 6003, ActivityID: 99, ActivityName: "Open"},
	} {
		for how, emit := range map[string]func(context.Context, Event) error{
			"Emit": e.Emit, "EmitRequired": e.EmitRequired, "EmitBlocking": e.EmitBlocking, "EmitBlockingBestEffort": e.EmitBlockingBestEffort,
		} {
			ev := Event{Action: a, Status: StatusSuccess}
			if a.ClassUID == 6003 {
				ev.Caller, ev.Operation = "signer", "Sign"
			}
			err := emit(context.Background(), ev)
			r, _ := refusal.Reason(err)
			if p, _ := refusal.FieldPath(err); err == nil || r != "schema" || !strings.HasPrefix(p, "action") {
				t.Errorf("%s, %s: err = %v at %q, want the refusal schema at action", name, how, err, p)
			}
		}
	}
	if len(tr.reqs) != 0 {
		t.Fatalf("%d messages reached the transport", len(tr.reqs))
	}
}

// verifies: REQ-AUDIT-099
//
// An event whose record the schema of its action refuses is not emitted: a member the row lacks, a required member
// missing, a value of the wrong shape. The refusal is invalid_argument, names the path and never the value, and nothing
// reaches the transport.
func TestTheEmitterRefusesARecordThatDoesNotValidateBeforeItEmitsIt(t *testing.T) {
	tr := &fakeTransport{}
	e, err := New(Config{Product: "example", Component: "key", Scope: "dev", Release: "0.0.0-test", Transport: tr, Table: closedTable(t), Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	sign := Action{Name: "key.sign", ClassUID: 6003, ActivityID: 99, ActivityName: "Sign"}
	good := map[string]any{"purpose": "token-signing", "key_id": "k1", "caller": "signer"}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range good {
			m[a] = b
		}
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	for name, s := range map[string]map[string]any{
		"a member the row lacks":    with("extra", "x"),
		"a required member missing": with("key_id", nil),
		"a name with a space":       with("key_id", "a secret value"),
		"an integer for a string":   with("purpose", int64(7)),
	} {
		err := e.Emit(context.Background(), Event{Action: sign, Status: StatusSuccess, ProductObject: s, Caller: "signer", Operation: "Sign"})
		var ce *connect.Error
		if r, _ := refusal.Reason(err); err == nil || r != "schema" || !errors.As(err, &ce) || ce.Code() != connect.CodeInvalidArgument {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		if p, _ := refusal.FieldPath(err); strings.Contains(p, "secret") {
			t.Errorf("%s: the path %q holds a value", name, p)
		}
	}
	if len(tr.reqs) != 0 {
		t.Fatalf("%d messages reached the transport", len(tr.reqs))
	}
	if err := e.Emit(context.Background(), Event{Action: sign, Status: StatusSuccess, ProductObject: good, Caller: "signer", Operation: "Sign"}); err != nil {
		t.Fatalf("a record that validates is emitted: %v", err)
	}
	if len(tr.reqs) != 1 {
		t.Fatalf("%d messages reached the transport", len(tr.reqs))
	}
}

// verifies: REQ-AUDIT-186
//
// The note of lost facts is part of the record, so the schema covers it: after a failure the next record carries
// unmapped.<product>.lost, and the emitter's own validation accepts the record that carries it.
func TestTheRecordThatCarriesTheNoteOfLostFactsValidates(t *testing.T) {
	tr := &fakeTransport{}
	e, err := New(Config{Product: "example", Component: "key", Scope: "dev", Release: "0.0.0-test", Transport: tr, Table: closedTable(t), Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	sign := Action{Name: "key.sign", ClassUID: 6003, ActivityID: 99, ActivityName: "Sign"}
	good := map[string]any{"purpose": "token-signing", "key_id": "k1", "caller": "signer"}
	e.recordLoss("", fixedNow.UnixMilli(), 1)
	if err := e.Emit(context.Background(), Event{Action: sign, Status: StatusSuccess, ProductObject: good, Caller: "signer", Operation: "Sign"}); err != nil {
		t.Fatalf("a record with the note: %v", err)
	}
	if len(tr.reqs) != 1 || !bytes.Contains(tr.reqs[0].GetMessage().GetRecord(), []byte(`"lost":{"process":`)) {
		t.Fatalf("the record does not carry the note: %v", tr.reqs)
	}
}
