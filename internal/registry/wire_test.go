package registry_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvity/audit/internal/registry"
)

// A catalogue read back over JSON says `registered_at`, as the reference
// promises, not `registeredAt`, as connect-go's default codec would.
func TestTheRegistrySpeaksSnakeCase(t *testing.T) {
	r := registryFor(t, nil, "wallet")
	if problems, err := r.Register(context.Background(), entry(walletDoc)); err != nil || len(problems) > 0 {
		t.Fatalf("register: %v %v", problems, err)
	}
	path, handler := registry.NewHandler(r)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	res, err := http.Post(server.URL+"/audit.v1.RegistryService/GetCatalogue", "application/json",
		strings.NewReader(`{"source": "wallet", "version": "1.0.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), `"registered_at"`) || strings.Contains(string(body), `"registeredAt"`) {
		t.Fatalf("not snake_case: %s", body)
	}
}
