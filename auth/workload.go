package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Workload identity: how a service in the cluster proves who it is to the
// writer and the registry.
//
// A workload presents its projected service-account token, a JWT the cluster
// signs with an audience of our choosing, and the JWT authenticator verifies it
// against the cluster's OIDC issuer like any other issuer. The subject is the
// service account, system:serviceaccount:<namespace>:<name>, which the kubelet
// vouches for and the workload cannot choose.
//
// That replaces a header a trusted upstream was to set. A header is a claim
// anyone who can reach the port can make; a signed token is one only the
// cluster can.

type principalKey struct{}

// WithPrincipal attaches a verified principal to a context.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal a Middleware verified, if any.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Middleware authenticates every request before it reaches next, and refuses
// the ones that do not verify with 401. The principal is on the request's
// context for the handler to read with PrincipalFrom.
//
// It refuses rather than passing an anonymous request through, because the
// handlers behind it stamp or check identity, and a handler that received no
// principal would have to decide on its own whether that is allowed — which is
// how one of them eventually decides wrongly.
func Middleware(a Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.Principal(r.Context(), r)
		if err != nil {
			http.Error(w, ErrUnauthenticated.Error(), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// SubjectFrom is the verified subject a Middleware put on the context, or ""
// when there is none. It is the writer's observer: who published a record,
// taken from the credential rather than from the record.
func SubjectFrom(ctx context.Context) string {
	p, _ := PrincipalFrom(ctx)
	return p.Subject
}

// Workload names the source one service account speaks for.
type Workload struct {
	// Issuer may be left empty when one issuer is trusted.
	Issuer string
	// Subject is the service account, system:serviceaccount:<ns>:<name>.
	Subject string
	// Source is the catalogue source this workload registers and writes as.
	Source string
}

// Workloads maps verified principals to sources.
type Workloads []Workload

// SourceOf is the source a principal speaks for, or "" for one that is not
// listed — which the registry refuses rather than guessing from the document.
func (ws Workloads) SourceOf(p Principal) string {
	for _, w := range ws {
		if w.Subject == p.Subject && (w.Issuer == "" || w.Issuer == p.Issuer) {
			return w.Source
		}
	}
	return ""
}

// SourceFrom is the source the context's verified principal speaks for, or ""
// when there is no principal or it is not listed. It is the registry's
// Identity.
func (ws Workloads) SourceFrom(ctx context.Context) string {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return ""
	}
	return ws.SourceOf(p)
}

// BoundTo checks the mapping against the trusted issuers, for the same reason
// rules are checked: with two issuers, an entry naming none would accept that
// subject from either, and the second may be one whose subjects anybody can
// choose.
func (ws Workloads) BoundTo(issuers []string) error {
	known := map[string]bool{}
	for _, is := range issuers {
		known[is] = true
	}
	for _, w := range ws {
		switch {
		case w.Subject == "" || w.Source == "":
			return fmt.Errorf("auth: a workload needs a subject and a source: %+v", w)
		case w.Issuer == "" && len(issuers) > 1:
			return fmt.Errorf("auth: workload %s names no issuer, and %d are trusted", w.Subject, len(issuers))
		case w.Issuer != "" && !known[w.Issuer]:
			return fmt.Errorf("auth: workload %s is for issuer %s, which is not trusted", w.Subject, w.Issuer)
		}
	}
	return nil
}

// TokenFile is an HTTP client that presents the token in a file as a bearer.
//
// The file is read on every request rather than once, because the kubelet
// rewrites a projected token before it expires and a client that cached the
// first one would start failing an hour into a long job. A file that cannot be
// read fails the request: sending it anonymously instead would only move the
// failure to the server, with a less useful message.
func TokenFile(path string) *http.Client {
	return &http.Client{Transport: tokenFile{path: path, next: http.DefaultTransport}}
}

type tokenFile struct {
	path string
	next http.RoundTripper
}

func (t tokenFile) RoundTrip(r *http.Request) (*http.Response, error) {
	raw, err := os.ReadFile(t.path)
	if err != nil {
		return nil, fmt.Errorf("auth: reading the token at %s: %w", t.path, err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return nil, errors.New("auth: the token file " + t.path + " is empty")
	}
	// A RoundTripper must not change the request it was given.
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+token)
	return t.next.RoundTrip(r)
}
