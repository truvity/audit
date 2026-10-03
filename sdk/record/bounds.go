package record

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"google.golang.org/protobuf/types/known/structpb"
)

// Bounds is what a record may not exceed. They exist so that one enormous
// request body cannot push a day of ordinary records out of a rolled object,
// and so that the cost of an audit trail is predictable per event.
type Bounds struct {
	// MaxBytes is the canonical size of the whole record.
	MaxBytes int
	// CaptureBytes is the canonical size of the request and of the response,
	// each on its own, before the pair is dropped.
	CaptureBytes int

	Targets           int
	ClientAddresses   int
	AttributeKeys     int
	AttributeKeyLen   int
	AttributeValueLen int

	IDLen        int
	NameLen      int
	ReasonLen    int
	CodeLen      int
	UserAgentLen int
}

// Default bounds. A deployment may lower them; raising them changes what the
// archive costs and is a decision, not a setting.
var Default = Bounds{
	MaxBytes:          256 << 10,
	CaptureBytes:      64 << 10,
	Targets:           32,
	ClientAddresses:   8,
	AttributeKeys:     50,
	AttributeKeyLen:   64,
	AttributeValueLen: 512,
	IDLen:             128,
	NameLen:           256,
	ReasonLen:         512,
	CodeLen:           64,
	UserAgentLen:      256,
}

// TruncationOrder is what is given up, in order, when a record is over
// MaxBytes. It is published because a reader of a truncated record is owed an
// account of what is missing and why.
var TruncationOrder = []string{"capture.response", "capture.request", "unmapped", "attributes", "outcome.reason"}

var attributeKeyUnsafe = regexp.MustCompile(`[^A-Za-z0-9_.:-]+`)

// Normalise makes a record well formed: control characters out of scalar
// fields, counts and lengths within bounds, and, if it is still too large,
// parts given up in TruncationOrder until it fits. It reports whether anything
// was given up, and sets capture.truncated when it was.
//
// Normalise never rejects; Check does that. An emitter runs both, in that
// order, so that the reason a record is refused is never "it was too long".
func Normalise(r *Record, b Bounds) bool {
	if r == nil {
		return false
	}
	truncated := false

	r.Id = clean(r.GetId(), b.IDLen)
	r.SchemaVersion = clean(r.GetSchemaVersion(), 32)
	r.CatalogueVersion = clean(r.GetCatalogueVersion(), 64)
	r.Source = clean(r.GetSource(), 64)
	r.Action = clean(r.GetAction(), 128)
	r.TenantId = clean(r.GetTenantId(), b.IDLen)
	r.Profile = clean(r.GetProfile(), 64)
	r.OriginHash = clean(r.GetOriginHash(), 128)

	if o := r.GetObserver(); o != nil {
		o.Id = clean(o.GetId(), b.IDLen)
		o.Version = clean(o.GetVersion(), 64)
		o.Instance = clean(o.GetInstance(), 128)
	}
	if o := r.GetOutcome(); o != nil {
		o.Reason = clean(o.GetReason(), b.ReasonLen)
		o.Code = clean(o.GetCode(), b.CodeLen)
	}
	if p := r.GetSubject(); p != nil {
		p.Kind = clean(p.GetKind(), 64)
		p.Id = clean(p.GetId(), b.IDLen)
	}
	if a := r.GetActor(); a != nil {
		a.Kind = clean(a.GetKind(), 64)
		a.Id = clean(a.GetId(), b.IDLen)
		a.SessionId = clean(a.GetSessionId(), b.IDLen)
		a.AuthMethod = clean(a.GetAuthMethod(), 64)
		a.CredentialHash = clean(a.GetCredentialHash(), 128)
	}
	if len(r.GetTargets()) > b.Targets {
		r.Targets = r.GetTargets()[:b.Targets]
		truncated = true
	}
	for _, t := range r.GetTargets() {
		t.Type = clean(t.GetType(), 64)
		t.Id = clean(t.GetId(), b.IDLen)
		t.Name = clean(t.GetName(), b.NameLen)
	}
	if c := r.GetContext(); c != nil {
		if len(c.GetClientAddresses()) > b.ClientAddresses {
			c.ClientAddresses = c.GetClientAddresses()[:b.ClientAddresses]
			truncated = true
		}
		for i, a := range c.GetClientAddresses() {
			c.ClientAddresses[i] = clean(a, 64)
		}
		c.UserAgent = clean(c.GetUserAgent(), b.UserAgentLen)
		c.RequestId = clean(c.GetRequestId(), b.IDLen)
		c.TraceId = clean(c.GetTraceId(), b.IDLen)
		c.SpanId = clean(c.GetSpanId(), b.IDLen)
	}
	if m := r.GetMeter(); m != nil {
		m.Name = clean(m.GetName(), 128)
		m.Quantity = clean(m.GetQuantity(), 64)
		m.Unit = clean(m.GetUnit(), 32)
	}
	if normaliseAttributes(r, b) {
		truncated = true
	}
	if capSlot(r.GetCapture(), b.CaptureBytes) {
		truncated = true
	}
	if shrink(r, b) {
		truncated = true
	}
	if truncated && r.GetCapture() != nil {
		r.Capture.Truncated = true
	}
	return truncated
}

// normaliseAttributes keeps the free-form map within its key count, key charset
// and value length. Keys are dropped in sorted order, so two replicas given the
// same record drop the same keys.
func normaliseAttributes(r *Record, b Bounds) bool {
	attrs := r.GetAttributes()
	if len(attrs) == 0 {
		return false
	}
	dropped := false
	out := make(map[string]string, len(attrs))
	for k, v := range attrs {
		key := attributeKeyUnsafe.ReplaceAllString(clean(k, b.AttributeKeyLen), "_")
		if key == "" {
			dropped = true
			continue
		}
		out[key] = clean(v, b.AttributeValueLen)
	}
	if len(out) > b.AttributeKeys {
		keys := make([]string, 0, len(out))
		for k := range out {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys[b.AttributeKeys:] {
			delete(out, k)
		}
		dropped = true
	}
	r.Attributes = out
	return dropped
}

// capSlot drops a request or response body that is too large on its own, before
// the whole-record budget is considered, so that one oversized body does not
// cost the record its attributes as well.
func capSlot(c *Capture, limit int) bool {
	if c == nil || limit <= 0 {
		return false
	}
	dropped := false
	if s := c.GetResponse(); s != nil && structSize(s) > limit {
		c.Response = nil
		dropped = true
	}
	if s := c.GetRequest(); s != nil && structSize(s) > limit {
		c.Request = nil
		dropped = true
	}
	return dropped
}

// shrink gives up parts in TruncationOrder until the record fits.
func shrink(r *Record, b Bounds) bool {
	if b.MaxBytes <= 0 || Size(r) <= b.MaxBytes {
		return false
	}
	for _, part := range TruncationOrder {
		switch part {
		case "capture.response":
			if r.GetCapture() != nil {
				r.Capture.Response = nil
			}
		case "capture.request":
			if r.GetCapture() != nil {
				r.Capture.Request = nil
			}
		case "unmapped":
			r.Unmapped = nil
		case "attributes":
			r.Attributes = nil
		case "outcome.reason":
			if o := r.GetOutcome(); o != nil {
				o.Reason = truncate(o.GetReason(), 64)
			}
		}
		if Size(r) <= b.MaxBytes {
			break
		}
	}
	return true
}

func structSize(s *structpb.Struct) int {
	b, err := Canonical(s)
	if err != nil {
		return 0
	}
	return len(b)
}

// clean removes the control characters that let a value forge a line in a text
// log, and cuts the value to length on a rune boundary.
func clean(s string, limit int) string {
	if s == "" {
		return s
	}
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return truncate(s, limit)
}

func truncate(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for len(cut) > 0 && !utf8ValidEnd(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

func utf8ValidEnd(s string) bool {
	r := []rune(s)
	if len(r) == 0 {
		return true
	}
	return r[len(r)-1] != '�' || strings.HasSuffix(s, "�")
}
