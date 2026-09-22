package main

import (
	"log/slog"
	"os"
	"strings"

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
func connectOptions(name string, o streamOptions) []nats.Option {
	opts := []nats.Option{
		nats.Name(name),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				slog.Error("disconnected from the stream", "error", err)
			}
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			slog.Info("reconnected to the stream", "url", c.ConnectedUrl())
		}),
	}
	if o.TokenFile != "" {
		opts = append(opts,
			// Read on every connect, because the token rotates: the kubelet
			// replaces a projected token before it expires, and the broker
			// drops a connection whose token has.
			nats.TokenHandler(tokenFromFile(o.TokenFile)),
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

// tokenFromFile is the token source: the file's contents, trimmed, read every
// time it is asked. A file that cannot be read yields no token, which the
// broker refuses, and the reconnect loop asks again; the failure is logged
// and the token never is.
func tokenFromFile(path string) nats.AuthTokenHandler {
	return func() string {
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Error("reading the stream token", "path", path, "error", err)
			return ""
		}
		return strings.TrimSpace(string(data))
	}
}
