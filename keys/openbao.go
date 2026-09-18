package keys

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// openbao is one connection to an OpenBAO (or Vault) transit engine, shared by
// the signer and the key provider so that the two cannot disagree about how a
// token is read or an error is reported.
type openbao struct {
	Address   string
	Mount     string
	Token     string
	TokenFile string
	HTTP      *http.Client
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

func (o openbao) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (o openbao) token() (string, error) {
	if o.TokenFile != "" {
		raw, err := os.ReadFile(o.TokenFile)
		if err != nil {
			return "", fmt.Errorf("keys: transit token: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	if o.Token == "" {
		return "", errors.New("keys: transit needs a token or a token file")
	}
	return o.Token, nil
}

// call makes one request to the transit engine and decodes its data.
func (o openbao) call(ctx context.Context, method, path string, body, into any) error {
	token, err := o.token()
	if err != nil {
		return err
	}
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(raw)
	}
	url := strings.TrimRight(o.Address, "/") + "/v1/" + o.mount() + "/" + path
	req, err := http.NewRequestWithContext(ctx, method, url, payload)
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", token)
	res, err := o.client().Do(req)
	if err != nil {
		return fmt.Errorf("keys: transit %s: %w", path, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
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
		return refusal
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
