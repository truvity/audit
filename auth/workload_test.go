package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/truvity/audit/auth"
	"github.com/truvity/audit/internal/authtest"
)

// The kubelet rewrites a projected token before it expires. A client that read
// the file once would start failing an hour into a long job, so the file is
// read on every request — and this checks the second request carries the
// second token.
func TestTokenFilePresentsTheCurrentToken(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
	}))
	t.Cleanup(server.Close)

	file := filepath.Join(t.TempDir(), "token")
	client := auth.TokenFile(file)
	for _, token := range []string{"first", "second"} {
		if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		res, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
	}
	if len(seen) != 2 || seen[0] != "Bearer first" || seen[1] != "Bearer second" {
		t.Fatalf("presented %q", seen)
	}
}

// A token that cannot be read fails the request here, with the path in the
// message, rather than going out anonymously and failing at the server with a
// message that says nothing about the file.
func TestTokenFileThatIsMissingFailsTheRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(server.Close)
	_, err := auth.TokenFile(filepath.Join(t.TempDir(), "absent")).Get(server.URL)
	if err == nil || called {
		t.Fatalf("an unreadable token went out anyway: err=%v called=%v", err, called)
	}
}

func TestMiddlewareRefusesAnUnverifiedCallerAndNamesAVerifiedOne(t *testing.T) {
	cluster := authtest.NewIssuer(t)
	authn, err := auth.NewJWT(context.Background(),
		[]auth.Issuer{{URL: cluster.URL, Audience: "audit"}}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	var got string
	handler := auth.Middleware(authn, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = auth.SubjectFrom(r.Context())
	}))

	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodPost, "/", nil))
	if anonymous.Code != http.StatusUnauthorized || got != "" {
		t.Fatalf("an anonymous caller reached the handler: %d %q", anonymous.Code, got)
	}

	sa := authtest.ServiceAccount("audit", "digest")
	verified := httptest.NewRecorder()
	handler.ServeHTTP(verified, bearer(cluster.Token(t, cluster.URL, sa, nil)))
	if verified.Code != http.StatusOK || got != sa {
		t.Fatalf("a verified caller: %d, subject %q", verified.Code, got)
	}
}

func TestAWorkloadEntryIsBoundToItsIssuer(t *testing.T) {
	ws := auth.Workloads{{Issuer: "https://cluster-a", Subject: "system:serviceaccount:w:api", Source: "wallet"}}
	same := auth.Principal{Issuer: "https://cluster-a", Subject: "system:serviceaccount:w:api"}
	other := auth.Principal{Issuer: "https://cluster-b", Subject: "system:serviceaccount:w:api"}
	if ws.SourceOf(same) != "wallet" || ws.SourceOf(other) != "" {
		t.Fatalf("same=%q other=%q", ws.SourceOf(same), ws.SourceOf(other))
	}
	// Two clusters can both have a namespace called w; an entry that names no
	// issuer would take either one's service account.
	loose := auth.Workloads{{Subject: "system:serviceaccount:w:api", Source: "wallet"}}
	if err := loose.BoundTo([]string{"https://cluster-a", "https://cluster-b"}); err == nil {
		t.Fatal("an entry naming no issuer was accepted with two trusted")
	}
}
