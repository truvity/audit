package authn_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/truvity/audit/authn"
	"github.com/truvity/audit/internal/authtest"
	"github.com/truvity/audit/sdk/auth"
)

func TestMiddlewareRefusesAnUnverifiedCallerAndNamesAVerifiedOne(t *testing.T) {
	cluster := authtest.NewIssuer(t)
	verifier, err := authn.NewJWT(context.Background(),
		[]authn.Issuer{{URL: cluster.URL, Audience: "audit"}}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	var got string
	handler := auth.Middleware(verifier, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
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
