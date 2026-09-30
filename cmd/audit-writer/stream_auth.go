package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// connectOptions are how both ends of the stream connect: the receiver that
// publishes and the writer that consumes. They are one list so that the two
// cannot drift — a token the receiver presents and the writer does not is a
// deployment that half works.
//
// The connection reconnects for as long as the process lives. A writer that
// gave up on the stream would go on answering its own health check while the
// backlog grew behind it. The first connection is not retried: a broker that
// is unreachable or refuses the token at start-up stops the process with the
// reason in its log, the same as a stream that is not there, and the pod's
// restart is the retry.
//
// Everything the client has to say goes through slog (o.Log, or the default). Its default for an
// asynchronous error is a bare line on stderr, which no log pipeline can
// attribute a level to.
func connectOptions(name string, o streamOptions) []nats.Option {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	var lease *tokenLease
	if o.TokenFile != "" {
		lease = newTokenLease(o.TokenFile, o.RefreshLead)
		lease.log = log
		if o.leaseHook != nil {
			o.leaseHook(lease)
		}
	}
	opts := []nats.Option{
		nats.Name(name),
		nats.MaxReconnects(-1),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			if errors.Is(err, nats.ErrAuthExpired) {
				// The broker's session is bound to the token it was opened
				// with. Expiry is the ordinary end of one, and the reconnect
				// that follows presents the renewed token.
				lease.sawExpiry()
				log.Info("the stream token expired; reconnecting with the renewed one")
				return
			}
			attrs := []any{"error", err}
			if sub != nil {
				attrs = append(attrs, "subject", sub.Subject)
			}
			log.Error("stream connection error", attrs...)
		}),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			switch {
			case err == nil:
				// A disconnect the client chose: a planned reconnect, or a
				// close. Nothing went wrong.
				log.Info("disconnected from the stream")
			case lease.expiredRecently():
				log.Info("disconnected from the stream after its token expired", "error", err)
			default:
				log.Error("disconnected from the stream", "error", err)
			}
		}),
		nats.ConnectHandler(func(c *nats.Conn) {
			lease.bind(c)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			lease.bind(c)
			log.Info("reconnected to the stream", "url", c.ConnectedUrl())
		}),
		nats.ClosedHandler(func(*nats.Conn) {
			lease.stop()
		}),
	}
	if lease != nil {
		opts = append(opts,
			// Read on every connect, because the token rotates: the kubelet
			// replaces a projected token before it expires, and the broker
			// drops a connection whose token has.
			nats.TokenHandler(lease.token),
			// The client's default is to stop reconnecting after the same
			// authorization error twice from one server. Here the token is a
			// file something else renews, so a refusal is a token that was
			// stale or unreadable for a moment, and the next attempt reads it
			// afresh; giving up would leave the process alive and deaf.
			nats.IgnoreAuthErrorAbort(),
		)
	}
	return opts
}

// defaultRefreshLead is how long before the presented token expires the
// connection is reopened with the renewed one. The kubelet renews a projected
// token at four fifths of its life, so by then the file has long held the new
// one; thirty seconds is room for the reconnect, not for the renewal.
const defaultRefreshLead = 30 * time.Second

// tokenLease reopens a connection before the token it presented expires.
//
// A broker that authenticates with the token binds the session to it, and
// ends the session when the token expires: the client learns of it from an
// error, is disconnected, and reconnects. Nothing is lost that way, but every
// hour looks like a fault in the log, and every publish in flight at that
// moment is failed back to its caller. Reconnecting a little before the
// expiry, on the client's own schedule, makes the same change of token an
// ordinary event.
//
// The expiry is read from the token's claims and not verified. The token is
// this process's own credential, read from a file only it and the kubelet can
// see; what is taken from it is only when to ask again, and the broker is the
// one that decides whether it is valid.
type tokenLease struct {
	path  string
	lead  time.Duration
	now   func() time.Time
	after func(time.Duration, func()) (stop func() bool)
	log   *slog.Logger

	mu      sync.Mutex
	conn    reconnector
	timer   func() bool
	expires time.Time
	expired bool
	stopped bool
}

// reconnector is the part of a connection the lease uses.
type reconnector interface {
	ForceReconnect() error
}

func newTokenLease(path string, lead time.Duration) *tokenLease {
	if lead <= 0 {
		lead = defaultRefreshLead
	}
	return &tokenLease{
		path: path,
		lead: lead,
		now:  time.Now,
		log:  slog.Default(),
		after: func(d time.Duration, f func()) func() bool {
			return time.AfterFunc(d, f).Stop
		},
	}
}

// token is the token source: the file's contents, trimmed, read every time it
// is asked. A file that cannot be read yields no token, which the broker
// refuses, and the reconnect loop asks again; the failure is logged and the
// token never is. Each token read is the one the next session will be bound
// to, so it is also what the reconnect is scheduled from.
func (l *tokenLease) token() string {
	data, err := os.ReadFile(l.path)
	if err != nil {
		l.log.Error("reading the stream token", "path", l.path, "error", err)
		return ""
	}
	token := strings.TrimSpace(string(data))
	l.presented(expiryOf(token))
	return token
}

// presented schedules the reconnect for a token about to be presented.
func (l *tokenLease) presented(expires time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	if l.timer != nil {
		l.timer()
		l.timer = nil
	}
	l.expires = expires
	if expires.IsZero() {
		// Not a token with an expiry: nothing to schedule, and a broker that
		// expires it anyway is reconnected to all the same.
		return
	}
	in := expires.Sub(l.now()) - l.lead
	if in <= 0 {
		// The file still holds a token about to expire, so reconnecting early
		// would present the same one again. The broker's expiry, and the
		// reconnect after it, reads the file again.
		l.log.Warn("the stream token expires too soon to reconnect before it; "+
			"the connection will be renewed when the broker expires it",
			"expires", expires.UTC().Format(time.RFC3339))
		return
	}
	l.timer = l.after(in, l.renew)
}

// renew reopens the connection, which reads the token file again.
func (l *tokenLease) renew() {
	l.mu.Lock()
	conn, expires, stopped := l.conn, l.expires, l.stopped
	l.timer = nil
	l.mu.Unlock()
	if stopped || conn == nil {
		return
	}
	l.log.Info("reconnecting to the stream before its token expires",
		"expires", expires.UTC().Format(time.RFC3339))
	if err := conn.ForceReconnect(); err != nil && !errors.Is(err, nats.ErrConnectionClosed) {
		l.log.Error("reconnecting to the stream before its token expires", "error", err)
	}
}

// bind remembers the connection to reopen, once it is established. A session
// that begins also ends any expiry the last one saw.
func (l *tokenLease) bind(c *nats.Conn) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.conn = c
	l.expired = false
}

// stop cancels the reconnect when the connection is closed for good.
func (l *tokenLease) stop() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopped = true
	if l.timer != nil {
		l.timer()
		l.timer = nil
	}
}

// sawExpiry notes that the broker expired the session, so that the disconnect
// following it is reported as the ordinary thing it is.
func (l *tokenLease) sawExpiry() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expired = true
}

func (l *tokenLease) expiredRecently() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.expired
}

// expiryOf reads the exp claim of a JWT without verifying it. Anything that is
// not a JWT with an expiry has none.
func expiryOf(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == "" {
		return time.Time{}
	}
	exp, err := claims.Exp.Float64()
	if err != nil || exp <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(exp), 0)
}
