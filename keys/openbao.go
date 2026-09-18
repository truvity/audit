package keys

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// JWTLogin is how a workload signs in to OpenBAO without a stored secret: it
// presents a JWT — normally its projected service-account token — to a JWT
// auth mount, under a role that binds the token's subject and audience and
// names the policies the session gets. It is the way the estate's workloads
// already reach OpenBAO, and it means there is no long-lived token anywhere to
// leak or rotate.
type JWTLogin struct {
	// Mount is the auth mount, e.g. jwt-devel: one per cluster, inside the
	// environment's namespace.
	Mount string
	// Role is the role on that mount.
	Role string
	// TokenFile holds the JWT. It is read at every login, because the kubelet
	// replaces a projected token before it expires.
	TokenFile string
}

// openbao is one connection to an OpenBAO (or Vault) transit engine, shared by
// the signer and the key provider so that the two cannot disagree about how a
// token is obtained or an error is reported.
type openbao struct {
	Address string
	Mount   string
	// Namespace is the OpenBAO namespace the engine and the auth mount live
	// in, sent on every request. Empty is the root namespace.
	Namespace string
	// CAFile is a PEM bundle trusted in addition to the system roots, for a
	// server whose certificate comes from a private chain.
	CAFile    string
	Token     string
	TokenFile string
	Login     *JWTLogin
	HTTP      *http.Client

	// state is the owner's: the client built from CAFile and the session a
	// login produced, kept across the calls that each build an openbao.
	state *baoState
}

// baoState is what outlives one call: the HTTP client, and the token a login
// returned with when it stops being worth using.
type baoState struct {
	mu        sync.Mutex
	client    *http.Client
	clientErr error
	token     string
	renewAt   time.Time
}

// transitError is a refusal from the engine, kept whole so that a caller can
// tell "no such key" from "that version is gone" without parsing a sentence it
// was handed as an error string.
type transitError struct {
	Path   string
	Status int
	Errors []string
}

func (e *transitError) Error() string {
	return fmt.Sprintf("keys: transit %s: %d: %s", e.Path, e.Status, strings.Join(e.Errors, "; "))
}

// says reports whether the engine's refusal contains a phrase. The engine
// reports "no such key" and "version too old" as 400s that differ only in
// their text, so the text is what there is to go on.
func (e *transitError) says(phrase string) bool {
	for _, m := range e.Errors {
		if strings.Contains(strings.ToLower(m), phrase) {
			return true
		}
	}
	return false
}

func (o openbao) mount() string {
	if o.Mount == "" {
		return "transit"
	}
	return strings.Trim(o.Mount, "/")
}

// check holds the credentials to one way of getting a token.
func (o openbao) check() error {
	ways := 0
	for _, set := range []bool{o.Token != "", o.TokenFile != "", o.Login != nil} {
		if set {
			ways++
		}
	}
	switch {
	case ways == 0:
		return errors.New("keys: transit needs a token, a token file or a JWT login")
	case ways > 1:
		return errors.New("keys: transit takes one of a token, a token file or a JWT login, not several")
	case o.Login != nil && (o.Login.Mount == "" || o.Login.Role == "" || o.Login.TokenFile == ""):
		return errors.New("keys: a transit JWT login needs a mount, a role and a token file")
	}
	return nil
}

func (o openbao) client() (*http.Client, error) {
	if o.HTTP != nil {
		return o.HTTP, nil
	}
	if o.state == nil {
		return &http.Client{Timeout: 30 * time.Second}, nil
	}
	o.state.mu.Lock()
	defer o.state.mu.Unlock()
	if o.state.client == nil && o.state.clientErr == nil {
		o.state.client, o.state.clientErr = newBaoClient(o.CAFile)
	}
	return o.state.client, o.state.clientErr
}

// newBaoClient trusts the system roots and, given one, a private bundle too:
// the estate's OpenBAO serves a certificate from its own chain, and a client
// that trusted only that chain would break the day the server is public.
func newBaoClient(caFile string) (*http.Client, error) {
	if caFile == "" {
		return &http.Client{Timeout: 30 * time.Second}, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("keys: transit CA bundle: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("keys: transit CA bundle %s holds no certificate", caFile)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}, nil
}

// token is the token for the next request: the one given, the one in the file,
// or the one the last login returned while it is still worth using.
func (o openbao) token(ctx context.Context) (string, error) {
	switch {
	case o.Login != nil:
		return o.session(ctx)
	case o.TokenFile != "":
		raw, err := os.ReadFile(o.TokenFile)
		if err != nil {
			return "", fmt.Errorf("keys: transit token: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	case o.Token != "":
		return o.Token, nil
	}
	return "", errors.New("keys: transit needs a token, a token file or a JWT login")
}

// session logs in when there is no token or the one there is has run through
// most of its lease. Logging in again before the lease ends, rather than when
// a call is refused, keeps a busy writer from failing a batch on the boundary.
func (o openbao) session(ctx context.Context) (string, error) {
	o.state.mu.Lock()
	if o.state.token != "" && (o.state.renewAt.IsZero() || time.Now().Before(o.state.renewAt)) {
		token := o.state.token
		o.state.mu.Unlock()
		return token, nil
	}
	o.state.mu.Unlock()

	jwt, err := os.ReadFile(o.Login.TokenFile)
	if err != nil {
		return "", fmt.Errorf("keys: transit login token: %w", err)
	}
	var out struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
		} `json:"auth"`
	}
	path := "auth/" + strings.Trim(o.Login.Mount, "/") + "/login"
	raw, err := o.do(ctx, http.MethodPost, path, "", map[string]string{
		"role": o.Login.Role,
		"jwt":  strings.TrimSpace(string(jwt)),
	})
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Auth.ClientToken == "" {
		return "", fmt.Errorf("keys: transit login on %s returned no token", o.Login.Mount)
	}

	o.state.mu.Lock()
	defer o.state.mu.Unlock()
	o.state.token = out.Auth.ClientToken
	o.state.renewAt = time.Time{}
	if lease := time.Duration(out.Auth.LeaseDuration) * time.Second; lease > 0 {
		o.state.renewAt = time.Now().Add(lease * 3 / 4)
	}
	return o.state.token, nil
}

// forget drops the session, so the next call logs in afresh.
func (o openbao) forget() {
	o.state.mu.Lock()
	defer o.state.mu.Unlock()
	o.state.token, o.state.renewAt = "", time.Time{}
}

// call makes one request to the transit engine and decodes its data.
//
// A session can end before its lease says — revoked, or the server restored
// from a snapshot — and the engine then refuses with 403. With a login, that
// is answered once by logging in again; a second refusal is the policy's.
func (o openbao) call(ctx context.Context, method, path string, body, into any) error {
	err := o.callOnce(ctx, method, path, body, into)
	var refusal *transitError
	if o.Login != nil && errors.As(err, &refusal) && refusal.Status == http.StatusForbidden {
		o.forget()
		err = o.callOnce(ctx, method, path, body, into)
	}
	return err
}

func (o openbao) callOnce(ctx context.Context, method, path string, body, into any) error {
	token, err := o.token(ctx)
	if err != nil {
		return err
	}
	raw, err := o.do(ctx, method, o.mount()+"/"+path, token, body)
	if err != nil {
		return err
	}
	if into == nil {
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("keys: transit %s: %w", path, err)
	}
	return json.Unmarshal(envelope.Data, into)
}

// do sends one request under the namespace and returns the body of a success.
func (o openbao) do(ctx context.Context, method, path, token string, body any) ([]byte, error) {
	client, err := o.client()
	if err != nil {
		return nil, err
	}
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(raw)
	}
	url := strings.TrimRight(o.Address, "/") + "/v1/" + path
	req, err := http.NewRequestWithContext(ctx, method, url, payload)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if o.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", o.Namespace)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keys: transit %s: %w", path, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 != 2 {
		refusal := &transitError{Path: path, Status: res.StatusCode}
		var body struct {
			Errors []string `json:"errors"`
		}
		if json.Unmarshal(raw, &body) == nil && len(body.Errors) > 0 {
			refusal.Errors = body.Errors
		} else {
			refusal.Errors = []string{strings.TrimSpace(string(raw))}
		}
		return nil, refusal
	}
	return raw, nil
}
