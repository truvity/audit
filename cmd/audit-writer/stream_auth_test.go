package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// The token is read every time it is asked for, because a projected token is
// replaced by the kubelet before it expires and the broker drops a connection
// whose token has: what the handler read at start-up would refuse a reconnect
// an hour later.
func TestTheTokenIsReadAfreshOnEveryConnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	source := tokenFromFile(path)

	if got := source(); got != "" {
		t.Fatalf("a missing file yielded %q, want no token", got)
	}
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := source(); got != "first" {
		t.Fatalf("token = %q, want the file's contents, trimmed", got)
	}
	if err := os.WriteFile(path, []byte("  second  "), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := source(); got != "second" {
		t.Fatalf("token = %q after rotation, want the new contents", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := source(); got != "" {
		t.Fatalf("a removed file yielded %q, want no token", got)
	}
}

// Without a token file the options are what they were: no handler, and no
// credentials on the wire. A deployment whose broker verifies nobody must not
// have to change.
func TestNoTokenFileMeansNoCredentials(t *testing.T) {
	opts := nats.GetDefaultOptions()
	for _, o := range connectOptions("audit-writer", streamOptions{}) {
		if err := o(&opts); err != nil {
			t.Fatal(err)
		}
	}
	if opts.TokenHandler != nil || opts.Token != "" {
		t.Fatal("no token file was given and the connection would still present a token")
	}
	if opts.MaxReconnect != -1 {
		t.Fatalf("MaxReconnect = %d, want -1: the stream is reconnected to for as long as the process lives", opts.MaxReconnect)
	}
	if opts.IgnoreAuthErrorAbort {
		t.Fatal("no token, so the auth-error abort should stay the client's default")
	}
}

// A broker that verifies who connects starts here with a token. It is the
// embedded server's own token check rather than a callout, because what is
// under test is that both ends of the stream present the file's token, not how
// a broker decides about it.
func verifyingStream(t *testing.T, token string) (string, jetstream.JetStream) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Port:          -1,
		JetStream:     true,
		StoreDir:      t.TempDir(),
		Authorization: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("the test server did not start")
	}
	t.Cleanup(srv.Shutdown)

	conn, err := nats.Connect(srv.ClientURL(), nats.Token(token))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	js, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name: "AUDIT", Subjects: []string{subject}, Storage: jetstream.FileStorage,
	}); err != nil {
		t.Fatal(err)
	}
	return srv.ClientURL(), js
}

func tokenFile(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Both ends of the stream present the token: the writer that consumes, and the
// receiver that publishes. Neither connects without it, which is the point of
// a broker that verifies.
func TestBothEndsPresentTheStreamToken(t *testing.T) {
	url, js := verifyingStream(t, "s3cret")
	publish(t, js, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	refused := opts(url, 10)
	if _, err := consume(ctx, refused, &target{}); err == nil {
		t.Fatal("the writer connected to a verifying broker with no token")
	}
	if _, _, err := publisherFor(ctx, refused); err == nil {
		t.Fatal("the receiver connected to a verifying broker with no token")
	}

	with := opts(url, 10)
	with.TokenFile = tokenFile(t, "s3cret")
	into := &target{}
	stop, err := consume(ctx, with, into)
	if err != nil {
		t.Fatalf("the writer did not connect with the token: %v", err)
	}
	defer stop()
	eventually(t, func() bool { return into.count() == 3 }, "the stream did not reach the writer")

	p, stopPublisher, err := publisherFor(ctx, with)
	if err != nil {
		t.Fatalf("the receiver did not connect with the token: %v", err)
	}
	defer stopPublisher()
	if p == nil {
		t.Fatal("no publisher")
	}
}

// A token the broker refuses stops the process at start-up with the reason,
// as a missing stream does: the pod's restart is the retry, and a process that
// sat reconnecting behind a healthy /healthz would be a backlog nobody sees.
func TestAWrongTokenStopsTheWriterAtStart(t *testing.T) {
	url, _ := verifyingStream(t, "s3cret")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wrong := opts(url, 10)
	wrong.TokenFile = tokenFile(t, "not-it")
	if _, err := consume(ctx, wrong, &target{}); err == nil {
		t.Fatal("a refused token must stop the writer, not leave it reconnecting")
	}
}
