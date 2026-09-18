package registry_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/emit"
	"github.com/truvity/audit/internal/authtest"
	"github.com/truvity/audit/internal/registry"
)

// The registry decides whose catalogue a document is from the caller's
// verified service account, end to end: a real client presenting its projected
// token from a file, the middleware verifying it against the cluster's issuer,
// and the registry mapping the service account to a source.
func TestTheRegistryBelievesTheServiceAccountNotTheCaller(t *testing.T) {
	cluster := authtest.NewIssuer(t)
	ctx := context.Background()
	authn, err := auth.NewJWT(ctx, []auth.Issuer{{URL: cluster.URL, Audience: "audit"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	workloads := auth.Workloads{
		{Subject: authtest.ServiceAccount("wallet", "wallet-api"), Source: "wallet"},
		{Subject: authtest.ServiceAccount("billing", "billing-api"), Source: "billing"},
	}
	r := registryFor(t, nil, "")
	r.Identity = workloads.SourceFrom
	path, handler := registry.NewHandler(r)
	mux := http.NewServeMux()
	mux.Handle(path, auth.Middleware(authn, handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	// as registers the wallet catalogue with a given client.
	as := func(client *http.Client) error {
		return emit.Register(ctx, emit.Registration{
			URL: server.URL, Source: "wallet", Version: "1.0.0",
			Document: []byte(walletDoc), HTTP: client,
		})
	}
	// tokenFor writes a service account's token where the kubelet would.
	tokenFor := func(namespace, name string) *http.Client {
		file := filepath.Join(t.TempDir(), "token")
		token := cluster.Token(t, cluster.URL, authtest.ServiceAccount(namespace, name), nil)
		if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return auth.TokenFile(file)
	}

	t.Run("its own service account registers its own catalogue", func(t *testing.T) {
		if err := as(tokenFor("wallet", "wallet-api")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("another workload cannot register it", func(t *testing.T) {
		err := as(tokenFor("billing", "billing-api"))
		if err == nil || !strings.Contains(err.Error(), "billing may not register a catalogue for wallet") {
			t.Fatalf("billing registered wallet's catalogue: %v", err)
		}
		// A refusal is an answer, and an application must be able to tell it
		// from a registry it could not reach.
		if !errors.Is(err, emit.ErrCatalogueRefused) {
			t.Fatalf("a refusal is not recognisable as one: %v", err)
		}
	})
	t.Run("a verified service account nobody listed is refused", func(t *testing.T) {
		err := as(tokenFor("wallet", "debug-shell"))
		if err == nil || !strings.Contains(err.Error(), "could not be verified") {
			t.Fatalf("an unlisted workload registered: %v", err)
		}
	})
	// What the registry used to believe: a header naming the source. Anyone
	// who can reach the port can set it, so it is now simply not read.
	t.Run("a header naming the source is not a credential", func(t *testing.T) {
		spoof := &http.Client{Transport: header{"Audit-Source": "wallet"}}
		err := as(spoof)
		if err == nil || !strings.Contains(err.Error(), "unauthenticated") {
			t.Fatalf("a bare header registered a catalogue: %v", err)
		}
		if errors.Is(err, emit.ErrCatalogueRefused) {
			t.Fatal("a transport failure was reported as the deployment refusing the catalogue")
		}
	})
}

// header sets headers on every request, as a spoofing caller would.
type header map[string]string

func (h header) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}
