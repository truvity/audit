package emit

import (
	"context"
	"net"
	"net/http"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/truvity/audit/record"
)

type contextKey struct{}

// Middleware records where each request came from, so that any record emitted
// while handling it carries its provenance without the handler passing it
// along by hand.
//
// trustedHops is how many entries at the near end of the forwarded chain belong
// to your own edge. Those are hops you operate and already know about; the
// entry just before them is the best account anyone has of the client. Getting
// this number wrong is how an audit trail comes to record a load balancer as
// the actor's address, so it is a deployment setting and has no safe default
// but zero: with no proxy in front, the peer is the client.
func Middleware(trustedHops int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := WithRequest(r.Context(), FromRequest(r, trustedHops))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// WithRequest carries provenance on a context.
func WithRequest(ctx context.Context, c *record.Context) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, c)
}

// RequestContext returns the provenance recorded for this request, or nil.
func RequestContext(ctx context.Context) *record.Context {
	c, _ := ctx.Value(contextKey{}).(*record.Context)
	return c
}

// FromRequest reads provenance from a request.
func FromRequest(r *http.Request, trustedHops int) *record.Context {
	if r == nil {
		return nil
	}
	c := &record.Context{
		ClientAddresses: clientChain(r, trustedHops),
		UserAgent:       r.Header.Get("User-Agent"),
		RequestId:       firstHeader(r, "X-Request-Id", "X-Correlation-Id", "X-Amzn-Trace-Id"),
	}
	c.TraceId, c.SpanId = traceParent(r.Header.Get("traceparent"))
	return c
}

// clientChain is the forwarded chain with your own edge removed, nearest last,
// so the last entry is the claimed client.
//
// Every entry before the trusted hops is hearsay: a client may put anything in
// the header. It is kept because a chain that disagrees with itself is evidence
// too, and dropped from the record only by a profile that says so.
func clientChain(r *http.Request, trustedHops int) []string {
	var chain []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(header, ",") {
			if addr := strings.TrimSpace(part); addr != "" {
				chain = append(chain, addr)
			}
		}
	}
	// The peer is the one hop nobody can forge, and it is the nearest.
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		chain = append(chain, host)
	} else if r.RemoteAddr != "" {
		chain = append(chain, r.RemoteAddr)
	}
	if trustedHops > 0 && len(chain) > trustedHops {
		chain = chain[:len(chain)-trustedHops]
	}
	return chain
}

// Client is the address the chain attributes the request to: the last entry,
// which is the one just before the hops you operate.
func Client(c *record.Context) string {
	addrs := c.GetClientAddresses()
	if len(addrs) == 0 {
		return ""
	}
	return addrs[len(addrs)-1]
}

func firstHeader(r *http.Request, names ...string) string {
	for _, n := range names {
		if v := r.Header.Get(n); v != "" {
			return v
		}
	}
	return ""
}

// traceParent reads the trace and span of a W3C traceparent header. A malformed
// one yields nothing rather than a guess: a wrong correlation identifier costs
// more than a missing one.
func traceParent(v string) (traceID, spanID string) {
	parts := strings.Split(v, "-")
	if len(parts) < 4 || len(parts[1]) != 32 || len(parts[2]) != 16 {
		return "", ""
	}
	if strings.Trim(parts[1], "0") == "" || strings.Trim(parts[2], "0") == "" {
		return "", ""
	}
	return parts[1], parts[2]
}

// applyRequestContext fills a record's provenance from the request being
// handled, unless the caller set it. The context is copied, because several
// records of one request must not share a value that normalising will trim.
func applyRequestContext(ctx context.Context, r *record.Record) {
	if r.GetContext() != nil {
		return
	}
	if c := RequestContext(ctx); c != nil {
		r.Context = proto.Clone(c).(*record.Context)
	}
}
