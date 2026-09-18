package writer_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/internal/authtest"
	"github.com/truvity/audit/record"
	"github.com/truvity/audit/sink"
)

// The observer of a record is whoever the cluster says published it, end to
// end: a job presenting its projected token over HTTP, the middleware verifying
// it, and the writer stamping the service account on the record — whatever the
// record claimed about itself.
func TestTheObserverIsTheServiceAccountThatPublished(t *testing.T) {
	cluster := authtest.NewIssuer(t)
	authn, err := auth.NewJWT(context.Background(),
		[]auth.Issuer{{URL: cluster.URL, Audience: "audit"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	b := buildWith(t, parts{identity: auth.SubjectFrom})
	path, handler := sink.NewHandler(b.writer)
	mux := http.NewServeMux()
	mux.Handle(path, auth.Middleware(authn, handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	sa := authtest.ServiceAccount("wallet", "wallet-api")
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(cluster.Token(t, cluster.URL, sa, nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	r := fresh(t)
	r.Observer.Id = "somebody-it-would-rather-be"
	if _, err := sink.NewClient(auth.TokenFile(file), server.URL).Write(context.Background(),
		&sink.Request{Records: []*record.Record{r}, Delivery: sink.Block}); err != nil {
		t.Fatal(err)
	}

	copies := 0
	for _, c := range decode(t, b.store) {
		if c.GetProfile() == "billing" {
			continue // billing keeps no observer
		}
		copies++
		if got := c.GetObserver().GetId(); got != sa {
			t.Fatalf("observer id = %q, want the service account %q", got, sa)
		}
	}
	if copies == 0 {
		t.Fatal("no copy carries an observer, so this checked nothing")
	}

	// And a caller with no token does not get a record in at all.
	if _, err := sink.NewClient(nil, server.URL).Write(context.Background(),
		&sink.Request{Records: []*record.Record{fresh(t)}, Delivery: sink.Block}); err == nil {
		t.Fatal("an anonymous caller wrote a record")
	}
}
