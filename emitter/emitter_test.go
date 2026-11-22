package emitter

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/truvity/atoom/common/call"
	"github.com/truvity/atoom/common/refusal"
	transportv1alpha1 "github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1"
	"github.com/truvity/atoom/transport/gen/atoom/transport/v1alpha1/transportv1alpha1connect"
)

var fixedNow = time.UnixMilli(1793750400000) // 2026-11-04T00:00:00Z

// fakeTransport records what the emitter sent and answers with err.
type fakeTransport struct {
	transportv1alpha1connect.TransportServiceClient
	mu   sync.Mutex
	reqs []*transportv1alpha1.EmitRequest
	err  error
}

func (f *fakeTransport) Emit(_ context.Context, r *connect.Request[transportv1alpha1.EmitRequest]) (*connect.Response[transportv1alpha1.EmitResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r.Msg)
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&transportv1alpha1.EmitResponse{}), nil
}

// fakeKeys answers Mac with a fixed value and records the request.
type fakeKeys struct {
	mu   sync.Mutex
	reqs []MacRequest
	mac  []byte
	err  error
}

func (f *fakeKeys) Mac(_ context.Context, r MacRequest) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	if f.err != nil {
		return nil, f.err
	}
	return f.mac, nil
}

// hmacKeys is a host that holds one key and answers Mac with the HMAC-SHA384 of the input under it, as the host of the
// seal block does under a tenant's pseudonym key, so the pseudonym depends on the key and the input.
type hmacKeys struct {
	key []byte
}

func (f hmacKeys) Mac(_ context.Context, r MacRequest) ([]byte, error) {
	h := hmac.New(sha512.New384, f.key)
	h.Write(r.Input)
	return h.Sum(nil), nil
}

// testLabel is the label the tests configure. The emitter has none of its own (record 0011 leaves the neutral label open).
const testLabel = "example-pseudonym-v1"

// pseudonymsOf is the Pseudonyms of a Mac, or nil for none.
func pseudonymsOf(m Mac) *Pseudonyms {
	if m == nil {
		return nil
	}
	return &Pseudonyms{Mac: m, Label: testLabel}
}

func testMAC() []byte {
	mac := make([]byte, 48)
	for i := range mac {
		mac[i] = byte(i)
	}
	return mac
}

func newTestEmitter(t *testing.T, tr transportv1alpha1connect.TransportServiceClient, keys Mac) *Emitter {
	t.Helper()
	e, err := New(Config{Product: "example", Component: "key", Scope: "dev", Release: "0.0.0-test", Transport: tr, Pseudonyms: pseudonymsOf(keys), Table: openTable(t), Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

var signEvent = Event{Action: Action{Name: "key.sign", ClassUID: 6003, ActivityID: 99, ActivityName: "Sign"}, Status: StatusSuccess, Caller: "signer", Operation: "Sign"}

// verifies: REQ-AUDIT-128
func TestPseudonymComesFromTheTenantsPseudonymKey(t *testing.T) {
	keys := &fakeKeys{mac: testMAC()}
	e := newTestEmitter(t, &fakeTransport{}, keys)
	ev := signEvent
	ev.Tenant = "acme"
	ev.Subject = &Subject{Tenant: "acme", Kind: "person", Value: "person-123"}
	rec, uid, err := e.Record(call.WithCorrelationID(context.Background(), "req-1"), ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys.reqs) != 1 {
		t.Fatalf("Mac calls = %d", len(keys.reqs))
	}
	m := keys.reqs[0]
	wantInput := pseudonymInput("dev", Subject{Tenant: "acme", Kind: "person", Value: "person-123"})
	if m.Purpose != "pseudonym" || m.Tenant != "acme" || m.Label != testLabel || !bytes.Equal(m.Input, wantInput) || m.Epoch != "202611" {
		t.Errorf("Mac request = %v", m)
	}
	var got struct {
		Resources []struct{ UID string } `json:"resources"`
		Metadata  struct {
			UID            string `json:"uid"`
			CorrelationUID string `json:"correlation_uid"`
		} `json:"metadata"`
		Time int64 `json:"time"`
	}
	if err := json.Unmarshal(rec, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Resources) != 1 || got.Resources[0].UID != "p1.202611.AAECAwQFBgcICQoLDA0ODw" {
		t.Errorf("resources = %v: an API Activity has no user, the subject is its resource", got.Resources)
	}
	if got.Time != fixedNow.UnixMilli() {
		t.Errorf("time = %d", got.Time)
	}
	if got.Metadata.UID != uid || got.Metadata.CorrelationUID != "req-1" {
		t.Errorf("metadata = %+v, uid %s", got.Metadata, uid)
	}
	// The raw subject is nowhere in the record.
	if bytes.Contains(rec, []byte("person-123")) {
		t.Error("the raw subject is in the record")
	}
}

var pseudonymRE = regexp.MustCompile(`^p1\.202611\.[A-Za-z0-9_-]{22}$`)

// An external actor (a person, a workload, a client) is recorded under a pseudonym that the Mac computes with a key, and
// under nothing else: the raw identifier is in neither the record nor the message handed to the transport, the
// pseudonym changes with the key, the tenant, the kind and the identifier, and an event with no subject records none.
//
// verifies: REQ-AUDIT-023
func TestAnExternalActorIsRecordedUnderAKeyedPseudonym(t *testing.T) {
	keyA, keyB := []byte("tenant key A"), []byte("tenant key B")
	subjects := []Subject{
		{Tenant: "acme", Kind: "person", Value: "person-123"},
		{Tenant: "acme", Kind: "person", Value: "ada.lovelace@example.org"}, // an e-mail address is a fine subject: it is hashed
		{Tenant: "acme", Kind: "workload", Value: "spiffe://example.org/ns/pay/sa/api"},
		{Tenant: "acme", Kind: "client", Value: "client-abc"},
	}
	emitWith := func(t *testing.T, key []byte, s Subject) (rec []byte, msg []byte) {
		t.Helper()
		tr := &fakeTransport{}
		e := newTestEmitter(t, tr, hmacKeys{key: key})
		ev := signEvent
		ev.Subject = &s
		if err := e.Emit(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
		if len(tr.reqs) != 1 {
			t.Fatalf("transport got %d messages", len(tr.reqs))
		}
		wire, err := proto.Marshal(tr.reqs[0])
		if err != nil {
			t.Fatal(err)
		}
		return tr.reqs[0].GetMessage().GetRecord(), wire
	}
	pseudonymOf := func(t *testing.T, rec []byte) string {
		t.Helper()
		var got struct {
			Resources []struct{ UID string } `json:"resources"`
		}
		if err := json.Unmarshal(rec, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Resources) != 1 {
			t.Fatalf("resources = %v, want the subject", got.Resources)
		}
		return got.Resources[0].UID
	}

	seen := map[string]Subject{}
	for _, s := range subjects {
		t.Run(s.Kind+"/"+s.Value, func(t *testing.T) {
			rec, wire := emitWith(t, keyA, s)
			got := pseudonymOf(t, rec)
			if !pseudonymRE.MatchString(got) {
				t.Fatalf("user.uid = %q, want p1.<YYYYMM>.<128 bits, base64url>", got)
			}
			// Computed here from the key and the input of record 0011: p1.<epoch>.<first 128 bits of HMAC-SHA384>.
			h := hmac.New(sha512.New384, keyA)
			h.Write(pseudonymInput("dev", s))
			if want := "p1.202611." + base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:16]); got != want {
				t.Errorf("user.uid = %q, want %q", got, want)
			}
			// Nothing of the raw identifier, in the record or in the whole message.
			for _, raw := range []string{s.Value, strings.SplitN(s.Value, "@", 2)[0]} {
				if bytes.Contains(rec, []byte(raw)) || bytes.Contains(wire, []byte(raw)) {
					t.Errorf("%q is in what was sent", raw)
				}
			}
			if other, dup := seen[got]; dup {
				t.Errorf("%+v and %+v share the pseudonym %s", s, other, got)
			}
			seen[got] = s
			// The same key and subject give the same pseudonym; another key gives another one: it is keyed.
			again, _ := emitWith(t, keyA, s)
			if pseudonymOf(t, again) != got {
				t.Error("the same key and subject gave two pseudonyms")
			}
			other, _ := emitWith(t, keyB, s)
			if pseudonymOf(t, other) == got {
				t.Error("another key gave the same pseudonym: it is not keyed")
			}
		})
	}

	t.Run("tenant and kind", func(t *testing.T) {
		base := Subject{Tenant: "acme", Kind: "person", Value: "x"}
		a, _ := emitWith(t, keyA, base)
		for name, s := range map[string]Subject{
			"tenant": {Tenant: "globex", Kind: "person", Value: "x"},
			"kind":   {Tenant: "acme", Kind: "client", Value: "x"},
		} {
			b, _ := emitWith(t, keyA, s)
			if pseudonymOf(t, a) == pseudonymOf(t, b) {
				t.Errorf("another %s gave the same pseudonym", name)
			}
		}
	})

	t.Run("no subject records no user", func(t *testing.T) {
		keys := &fakeKeys{mac: testMAC()}
		tr := &fakeTransport{}
		e := newTestEmitter(t, tr, keys)
		if err := e.Emit(context.Background(), signEvent); err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(tr.reqs[0].GetMessage().GetRecord(), &got); err != nil {
			t.Fatal(err)
		}
		if _, has := got["user"]; has || len(keys.reqs) != 0 {
			t.Errorf("user = %v, Mac calls = %d", got["user"], len(keys.reqs))
		}
	})
}

var uuidV7RE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// The identifier of a record, metadata.uid, is a UUIDv7 in the lower-case form of RFC 9562 (version 7, variant 10),
// its first 48 bits are the emitter's clock in milliseconds, and the emitter sets it: the event has no member for an
// id, two identical events get two ids that increase, and the message carries the id the record holds.
//
// verifies: REQ-AUDIT-051
func TestTheRecordIdIsAUUIDv7TheEmitterSets(t *testing.T) {
	tr := &fakeTransport{}
	e := newTestEmitter(t, tr, nil)
	for range 3 {
		if err := e.Emit(context.Background(), signEvent); err != nil {
			t.Fatal(err)
		}
	}
	_, viaRecord, err := e.Record(context.Background(), signEvent)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, r := range tr.reqs {
		var rec struct {
			Metadata struct {
				UID string `json:"uid"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(r.GetMessage().GetRecord(), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Metadata.UID != r.GetMessage().GetUid() {
			t.Errorf("metadata.uid = %q, message uid = %q", rec.Metadata.UID, r.GetMessage().GetUid())
		}
		ids = append(ids, rec.Metadata.UID)
	}
	ids = append(ids, viaRecord)
	for i, id := range ids {
		if !uuidV7RE.MatchString(id) {
			t.Fatalf("id %q is not a UUIDv7 in the form of RFC 9562", id)
		}
		ms, err := strconv.ParseUint(strings.ReplaceAll(id[:13], "-", ""), 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		if ms != uint64(fixedNow.UnixMilli()) {
			t.Errorf("id %q carries %d ms, want the emitter's clock %d", id, ms, fixedNow.UnixMilli())
		}
		if i > 0 && id <= ids[i-1] {
			t.Errorf("id %q does not follow %q", id, ids[i-1])
		}
	}
}

func TestPseudonymVectors(t *testing.T) {
	var f recordFile
	readVectors(t, "record-vectors.json", &f)
	if len(f.Pseudonyms) == 0 {
		t.Fatal("no pseudonym vectors")
	}
	for _, v := range f.Pseudonyms {
		mac, err := hex.DecodeString(v.MacHex)
		if err != nil {
			t.Fatal(err)
		}
		keys := &fakeKeys{mac: mac}
		e, err := New(Config{Product: "example", Component: "key", Scope: "dev", Release: "r", Transport: &fakeTransport{}, Pseudonyms: pseudonymsOf(keys), Table: openTable(t), Now: func() time.Time { return time.UnixMilli(v.TimeMS) }})
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.pseudonym(context.Background(), Subject{Tenant: "acme", Kind: "person", Value: "x"}, e.now())
		if err != nil {
			t.Fatal(err)
		}
		if got != v.Expected || keys.reqs[0].Epoch != v.Epoch {
			t.Errorf("pseudonym = %s (epoch %s), want %s (epoch %s)", got, keys.reqs[0].Epoch, v.Expected, v.Epoch)
		}
	}
}

// verifies: REQ-AUDIT-023
func TestSubjectWithoutKeysIsAnErrorAndNothingIsSent(t *testing.T) {
	tr := &fakeTransport{}
	e := newTestEmitter(t, tr, nil)
	ev := signEvent
	ev.Subject = &Subject{Tenant: "acme", Kind: "person", Value: "person-123"}
	for name, call := range map[string]func() error{
		"Record":       func() error { _, _, err := e.Record(context.Background(), ev); return err },
		"Emit":         func() error { return e.Emit(context.Background(), ev) },
		"EmitBlocking": func() error { return e.EmitBlocking(context.Background(), ev) },
	} {
		err := call()
		if err == nil {
			t.Fatalf("%s: a subject without Keys was accepted", name)
		}
		if reason, _ := refusal.Reason(err); reason != "keys" {
			t.Errorf("%s: reason = %q (%v)", name, reason, err)
		}
	}
	if len(tr.reqs) != 0 {
		t.Fatal("something was sent")
	}
}

func TestKeyFailureStopsTheEmit(t *testing.T) {
	keys := &fakeKeys{err: connect.NewError(connect.CodeUnavailable, errors.New("down"))}
	tr := &fakeTransport{}
	e := newTestEmitter(t, tr, keys)
	ev := signEvent
	ev.Subject = &Subject{Tenant: "acme", Kind: "person", Value: "p"}
	err := e.Emit(context.Background(), ev)
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("err = %v", err)
	}
	if len(tr.reqs) != 0 {
		t.Fatal("something was sent without a pseudonym")
	}
}

// verifies: REQ-AUDIT-188
//
// A refusal that is worth more than the name of its subject is written when the Mac cannot make the pseudonym: the record goes
// without a subject, and an Authentication says `unknown` in the user, where without the flag the emit fails and nothing is
// sent. The fact is not lost, so nothing is counted.
func TestARecordWhoseSubjectIsBestEffortIsWrittenWhenTheMacCannotNameIt(t *testing.T) {
	keys := &fakeKeys{err: connect.NewError(connect.CodeUnavailable, errors.New("down"))}
	tr := &fakeTransport{}
	e, err := New(Config{Product: "example", Component: "session", Scope: "dev", Release: "0.0.0-test", Transport: tr, Pseudonyms: pseudonymsOf(keys), Table: authTable(t), Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	ev := Event{Action: Action{Name: "session.signin", ClassUID: 3002, ActivityID: 1}, Status: StatusFailure, StatusCode: "client", Tenant: "acme"}
	ev.Subject = &Subject{Tenant: "acme", Kind: "client", Value: "c"}
	if err := e.Emit(context.Background(), ev); err == nil {
		t.Fatal("the emit of a subject that must be named did not fail")
	}
	if len(tr.reqs) != 0 {
		t.Fatal("something was sent without a pseudonym")
	}
	ev.SubjectBestEffort = true
	if err := e.Emit(context.Background(), ev); err != nil {
		t.Fatalf("a best-effort subject: %v", err)
	}
	if len(tr.reqs) != 1 {
		t.Fatalf("requests = %d, want the record", len(tr.reqs))
	}
	var rec struct {
		User struct {
			UID    string `json:"uid"`
			TypeID int    `json:"type_id"`
		} `json:"user"`
		Unmapped *struct{} `json:"unmapped"`
	}
	if err := json.Unmarshal(tr.reqs[0].GetMessage().GetRecord(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.User.UID != "unknown" || rec.User.TypeID != 0 {
		t.Errorf("user = %+v, want unknown with type_id 0", rec.User)
	}
	// Only the first, failed emit is a lost fact; the second was written.
	if got := e.Lost(); got != 1 {
		t.Errorf("lost = %d, want 1 (the emit that failed)", got)
	}
}

// verifies: REQ-AUDIT-188
//
// A subject is optional only on a refusal: the flag on a success event is refused, and nothing is sent, because a success that
// went without the name of its subject would say less than it should and nobody would be told.
func TestASuccessCannotLeaveItsSubjectBestEffort(t *testing.T) {
	tr := &fakeTransport{}
	e := newTestEmitter(t, tr, &fakeKeys{mac: testMAC()})
	ev := signEvent
	ev.Subject = &Subject{Tenant: "acme", Kind: "person", Value: "p"}
	ev.SubjectBestEffort = true
	err := e.Emit(context.Background(), ev)
	reason, _ := refusal.Reason(err)
	path, _ := refusal.FieldPath(err)
	if err == nil || reason != "schema" || path != "subject_best_effort" {
		t.Fatalf("err = %v (reason %q, path %q), want schema at subject_best_effort", err, reason, path)
	}
	if len(tr.reqs) != 0 {
		t.Fatal("something was sent")
	}
	ev.Status, ev.StatusCode = StatusFailure, "grant"
	if err := e.Emit(context.Background(), ev); err != nil {
		t.Fatalf("a failure with the flag: %v", err)
	}
}

// warnLog collects the records of the default logger for the length of a test.
type warnLog struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (w *warnLog) Enabled(context.Context, slog.Level) bool { return true }
func (w *warnLog) Handle(_ context.Context, r slog.Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.recs = append(w.recs, r)
	return nil
}
func (w *warnLog) WithAttrs([]slog.Attr) slog.Handler { return w }
func (w *warnLog) WithGroup(string) slog.Handler      { return w }

// verifies: REQ-AUDIT-188
//
// While the host of the Mac is down every refusal that names a subject goes without it, and a warning for each would be a flood: one in a
// minute, with how many others went without a subject since the last.
func TestTheWarningThatASubjectWentUnnamedIsLimited(t *testing.T) {
	logs := &warnLog{}
	prev := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(prev) })

	now := fixedNow
	keys := &fakeKeys{err: connect.NewError(connect.CodeUnavailable, errors.New("down"))}
	tr := &fakeTransport{}
	e, err := New(Config{Product: "example", Component: "session", Scope: "dev", Release: "0.0.0-test", Transport: tr, Pseudonyms: pseudonymsOf(keys), Table: authTable(t), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ev := Event{Action: Action{Name: "session.signin", ClassUID: 3002, ActivityID: 1}, Status: StatusFailure, StatusCode: "client", Tenant: "acme",
		Subject: &Subject{Tenant: "acme", Kind: "client", Value: "c"}, SubjectBestEffort: true}
	warnings := func() (n int, since int64) {
		logs.mu.Lock()
		defer logs.mu.Unlock()
		for _, r := range logs.recs {
			if r.Level == slog.LevelWarn && strings.Contains(r.Message, "could not be named") {
				n++
				r.Attrs(func(a slog.Attr) bool {
					if a.Key == "since_last_warning" {
						since = a.Value.Int64()
					}
					return true
				})
			}
		}
		return n, since
	}
	for range 5 {
		if err := e.Emit(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := warnings(); n != 1 {
		t.Fatalf("%d warnings for 5 records in one instant, want 1", n)
	}
	now = now.Add(subjectWarnEvery + time.Second)
	if err := e.Emit(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if n, since := warnings(); n != 2 || since != 4 {
		t.Fatalf("%d warnings, the last saying %d others since the first; want 2 and 4", n, since)
	}
	if len(tr.reqs) != 6 {
		t.Fatalf("%d records sent, want all 6: the warning limit is not a record limit", len(tr.reqs))
	}
}

func TestEmitAndEmitBlockingSetTheFlagAndSendTheRecordOnce(t *testing.T) {
	tr := &fakeTransport{}
	e := newTestEmitter(t, tr, nil)
	if err := e.Emit(context.Background(), signEvent); err != nil {
		t.Fatal(err)
	}
	if err := e.EmitBlocking(context.Background(), signEvent); err != nil {
		t.Fatal(err)
	}
	if len(tr.reqs) != 2 || tr.reqs[0].GetBlocking() || !tr.reqs[1].GetBlocking() {
		t.Fatalf("requests = %v", tr.reqs)
	}
	for _, r := range tr.reqs {
		var md struct {
			Metadata struct {
				UID string `json:"uid"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(r.GetMessage().GetRecord(), &md); err != nil {
			t.Fatal(err)
		}
		if md.Metadata.UID == "" || md.Metadata.UID != r.GetMessage().GetUid() {
			t.Errorf("message uid %q, record uid %q", r.GetMessage().GetUid(), md.Metadata.UID)
		}
		if len(r.GetMessage().GetUsage()) != 0 {
			t.Error("a usage part was sent")
		}
	}
	if tr.reqs[0].GetMessage().GetUid() == tr.reqs[1].GetMessage().GetUid() {
		t.Error("two emits share a uid")
	}
}

// verifies: REQ-AUDIT-033
func TestATransportFailureIsReturnedAndNeverRetried(t *testing.T) {
	want := connect.NewError(connect.CodeUnavailable, errors.New("spool full"))
	tr := &fakeTransport{err: want}
	e := newTestEmitter(t, tr, nil)
	for _, call := range []func(context.Context, Event) error{e.Emit, e.EmitBlocking} {
		err := call(context.Background(), signEvent)
		if connect.CodeOf(err) != connect.CodeUnavailable || !errors.Is(err, want) {
			t.Fatalf("err = %v", err)
		}
	}
	if len(tr.reqs) != 2 {
		t.Fatalf("Emit calls = %d, want one per call (no retry)", len(tr.reqs))
	}
}

func TestNewRefusesAnIncompleteConfig(t *testing.T) {
	ok := Config{Product: "example", Component: "key", Scope: "dev", Release: "r", Transport: &fakeTransport{}, Table: openTable(t)}
	if _, err := New(ok); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*Config){
		"no product": func(c *Config) { c.Product = "" }, "no component": func(c *Config) { c.Component = "" },
		"no scope": func(c *Config) { c.Scope = "" }, "no release": func(c *Config) { c.Release = "" },
		"no transport": func(c *Config) { c.Transport = nil }, "no table": func(c *Config) { c.Table = nil },
		"no label":     func(c *Config) { c.Pseudonyms = &Pseudonyms{Mac: &fakeKeys{}} },
		"no mac":       func(c *Config) { c.Pseudonyms = &Pseudonyms{Label: testLabel} },
		"long product": func(c *Config) { c.Product = strings.Repeat("p", StringMax+1) },
	} {
		c := ok
		mod(&c)
		if _, err := New(c); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestEventChecks(t *testing.T) {
	cases := map[string]func(*Event){
		"no action name":     func(e *Event) { e.Action.Name = "" },
		"class below 1000":   func(e *Event) { e.Action.ClassUID = 3 },
		"activity above 99":  func(e *Event) { e.Action.ActivityID = 100 },
		"status 0":           func(e *Event) { e.Status = 0 },
		"long status code":   func(e *Event) { e.StatusCode = string(make([]byte, StringMax+1)) },
		"e-mail in tenant":   func(e *Event) { e.Tenant = "a@b.example" },
		"e-mail in decision": func(e *Event) { e.Policy = &PolicyRef{DecisionID: "a@b.example"} },
		"empty subject":      func(e *Event) { e.Subject = &Subject{Tenant: "acme", Kind: "person"} },
		"unsupported int":    func(e *Event) { e.ProductObject = map[string]any{"n": 3} },
		"a float":            func(e *Event) { e.ProductObject = map[string]any{"n": 1.5} },
		"a list of any":      func(e *Event) { e.ProductObject = map[string]any{"n": []any{"a"}} },
	}
	for name, mod := range cases {
		ev := signEvent
		mod(&ev)
		if err := validateEvent(ev, "example"); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// An e-mail address is a fine subject: it is hashed, and the record never carries it.
	ev := signEvent
	ev.Subject = &Subject{Tenant: "acme", Kind: "person", Value: "alice@example.com"}
	if err := validateEvent(ev, "example"); err != nil {
		t.Errorf("an e-mail address as the subject value: %v", err)
	}
}

func TestTheRecordIsTheSameWhateverTheMapOrder(t *testing.T) {
	e := newTestEmitter(t, &fakeTransport{}, nil)
	ev := signEvent
	ev.ProductObject = map[string]any{"b": int64(1), "a": "x", "c": map[string]any{"z": true, "y": []string{"2", "1"}}}
	var first []byte
	for range 20 {
		rec, _, err := e.Record(context.Background(), ev)
		if err != nil {
			t.Fatal(err)
		}
		// The uid differs per call; compare the part after it.
		var m map[string]any
		_ = json.Unmarshal(rec, &m)
		delete(m["metadata"].(map[string]any), "uid")
		b, _ := Canonical(m)
		if first == nil {
			first = b
		} else if string(first) != string(b) {
			t.Fatalf("records differ:\n%s\n%s", first, b)
		}
	}
}

func TestSubjectNeedsAKindAndTheActivityNeedsAName(t *testing.T) {
	ev := signEvent
	ev.Subject = &Subject{Tenant: "acme", Value: "x"}
	if err := validateEvent(ev, "example"); err == nil {
		t.Error("a subject without a kind was accepted")
	}
	ev = signEvent
	ev.Action.ActivityName = ""
	if err := validateEvent(ev, "example"); err == nil {
		t.Error("activity 99 without an activity name was accepted")
	}
}

// verifies: REQ-AUDIT-120
func TestThePolicyDeclaresItsProfileAndNeedsADecisionID(t *testing.T) {
	e := newTestEmitter(t, &fakeTransport{}, nil)
	ev := signEvent
	ev.Policy = &PolicyRef{DecisionID: "d-1", Digest: "sha384:x", PolicyIDs: []string{"p-1"}, Decision: DecisionDenied}
	rec, _, err := e.Record(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Metadata struct {
			Profiles []string `json:"profiles"`
		} `json:"metadata"`
		ActionID       int `json:"action_id"`
		Policy         struct{ UID, Version string }
		Authorizations []struct {
			Decision string
			Policy   struct{ UID string }
		}
	}
	if err := json.Unmarshal(rec, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Metadata.Profiles) != 1 || got.Metadata.Profiles[0] != "security_control" || got.ActionID != 2 ||
		len(got.Authorizations) != 1 || got.Authorizations[0].Decision != "Denied" || got.Authorizations[0].Policy.UID != "p-1" {
		t.Errorf("record = %s", rec)
	}
	// Without a policy the record declares no profile.
	rec, _, err = e.Record(context.Background(), signEvent)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rec, []byte("profiles")) || bytes.Contains(rec, []byte("action_id")) {
		t.Errorf("a record without a policy declares a profile: %s", rec)
	}
	ev.Policy = &PolicyRef{Digest: "sha384:x"}
	if err := validateEvent(ev, "example"); err == nil {
		t.Error("a digest without a decision id was accepted")
	}
	ev.Policy = &PolicyRef{DecisionID: "d", Decision: 7}
	if err := validateEvent(ev, "example"); err == nil {
		t.Error("an unknown decision was accepted")
	}
}

// The product names the product object and metadata.product.name, and the scope and the component make the actor.
func TestTheProductScopeAndComponentAreTheConfiguration(t *testing.T) {
	e, err := New(Config{Product: "other", Component: "signer", Scope: "prod", Release: "1.0.0", Transport: &fakeTransport{}, Table: openTable(t)})
	if err != nil {
		t.Fatal(err)
	}
	ev := signEvent
	ev.ProductObject = map[string]any{"k": "v"}
	rec, _, err := e.Record(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Metadata struct {
			LogName string `json:"log_name"`
			Product struct {
				Name string `json:"name"`
			} `json:"product"`
		} `json:"metadata"`
		Actor struct {
			User struct {
				UID string `json:"uid"`
			} `json:"user"`
		} `json:"actor"`
		Unmapped map[string]map[string]any `json:"unmapped"`
	}
	if err := json.Unmarshal(rec, &got); err != nil {
		t.Fatal(err)
	}
	if got.Metadata.Product.Name != "other" || got.Metadata.LogName != "prod" || got.Actor.User.UID != "signer@prod" ||
		len(got.Unmapped) != 1 || got.Unmapped["other"]["k"] != "v" {
		t.Fatalf("record = %s", rec)
	}
	ev.ProductObject = map[string]any{"n": 1.5}
	_, _, err = e.Record(context.Background(), ev)
	if p, _ := refusal.FieldPath(err); p != "unmapped.other.n" {
		t.Fatalf("err = %v at %q, want the path unmapped.other.n", err, p)
	}
}

// A product whose server keeps the call elsewhere gives its own reader of the correlation id, and an id that is not of
// the form is dropped.
func TestAProductGivesItsOwnReaderOfTheCorrelationID(t *testing.T) {
	for id, want := range map[string]string{"req-product": "req-product", "not valid!": ""} {
		e, err := New(Config{
			Product: "example", Component: "key", Scope: "dev", Release: "r", Transport: &fakeTransport{}, Table: openTable(t),
			Correlation: func(context.Context) string { return id },
		})
		if err != nil {
			t.Fatal(err)
		}
		rec, _, err := e.Record(context.Background(), signEvent)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Metadata struct {
				CorrelationUID string `json:"correlation_uid"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(rec, &got); err != nil {
			t.Fatal(err)
		}
		if got.Metadata.CorrelationUID != want {
			t.Errorf("%q: correlation_uid = %q, want %q", id, got.Metadata.CorrelationUID, want)
		}
	}
}

// BuildRecord makes the bytes of the record vectors from the public inputs.
func TestBuildRecordIsThePureSerialiser(t *testing.T) {
	var f recordFile
	readVectors(t, "record-vectors.json", &f)
	for _, c := range f.Cases {
		if c.Lost != nil {
			continue // BuildRecord carries no note
		}
		if validateEvent(c.Event.event(t), c.Product) != nil {
			continue // a vector of the layout alone, which the checks of an event refuse
		}
		got, err := BuildRecord(RecordInput{
			Product: c.Product, Component: c.Component, Scope: c.Scope, Release: c.Release, UID: c.UID, TimeMS: c.TimeMS,
			Correlation: c.Correlation, Pseudonym: c.Pseudonym, Event: c.Event.event(t),
		})
		if err != nil || string(got) != c.Expected {
			t.Errorf("%s: %v\n got %s\nwant %s", c.Name, err, got, c.Expected)
		}
	}
}
