package keys

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Transit keeps every tenant's key for every purpose in an OpenBAO (or Vault)
// transit engine, and asks the engine for each pseudonym.
//
// The key material never leaves the engine: a pseudonym is the engine's HMAC
// of the identifier under the key, and a sealed identifier is its encryption.
// That removes both faults of Local at once. Every replica asks the same
// engine, so they cannot mint different keys for one tenant, and there is no
// directory to lose. What a deployment gives up is a round trip per
// pseudonym.
//
// A key is named <prefix>.<purpose>.<tenant>, so that the engine's policy —
// not this code — decides who may use which purpose: a role granted
// transit/hmac/audit.security.* can pseudonymise for the security profile and
// for nothing else. See docs/operations/openbao-keys.md for the policies.
//
// Keys are never rotated, as everywhere in this package, and every call is
// pinned to the key's first version so that a rotation by somebody else
// changes nothing. Destroy is what ends a key: it rotates it once and trims
// the first version away. The key's name stays behind, holding a version
// nothing uses, which is what keeps a later call from creating a fresh key
// under the old name and handing the same person a second identity — the
// marker Local writes beside its files, kept by the engine instead.
type Transit struct {
	// Address is the server, e.g. https://openbao.example:8200.
	Address string
	// Mount is where the transit engine is mounted. Empty means "transit".
	Mount string
	// Prefix starts every key's name. Empty means "audit".
	Prefix string
	// Token authenticates; TokenFile, read on every call, is for a token an
	// agent keeps renewed. One of the two.
	Token     string
	TokenFile string
	HTTP      *http.Client
}

// transitVersion is the version every pseudonym and seal is made under.
const transitVersion = 1

// transitPurpose is what a purpose may be under this provider. The name is
// <prefix>.<purpose>.<tenant>, and a purpose with a dot in it would let
// ("a.b", "c") and ("a", "b.c") name the same key.
var transitPurpose = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// NewTransit returns a provider after checking that it can reach the engine
// with the credentials it was given — a writer that finds out on its first
// record has already accepted a batch it cannot pseudonymise.
func NewTransit(ctx context.Context, t *Transit) (*Transit, error) {
	if t.Address == "" {
		return nil, errors.New("keys: the transit provider needs an address")
	}
	if _, err := t.conn().token(); err != nil {
		return nil, err
	}
	// lookup-self is in every token's default policy, so this proves the
	// server answers and the token is alive without asking for more rights
	// than the provider will use.
	self := openbao{Address: t.Address, Mount: "auth/token", HTTP: t.HTTP, Token: t.Token, TokenFile: t.TokenFile}
	if err := self.call(ctx, http.MethodGet, "lookup-self", nil, nil); err != nil {
		return nil, fmt.Errorf("keys: the transit engine does not take this token: %w", err)
	}
	return t, nil
}

func (t *Transit) conn() openbao {
	return openbao{Address: t.Address, Mount: t.Mount, Token: t.Token, TokenFile: t.TokenFile, HTTP: t.HTTP}
}

// name is the transit key for a tenant and purpose.
func (t *Transit) name(tenant string, purpose Purpose) (string, error) {
	if err := checkName(tenant, purpose); err != nil {
		return "", err
	}
	if !transitPurpose.MatchString(string(purpose)) {
		return "", fmt.Errorf("keys: purpose %q cannot name a transit key: letters, digits, _ and - only", purpose)
	}
	prefix := t.Prefix
	if prefix == "" {
		prefix = "audit"
	}
	return prefix + "." + string(purpose) + "." + tenant, nil
}

// Pseudonym implements Provider.
func (t *Transit) Pseudonym(ctx context.Context, tenant string, purpose Purpose, identifier string) (string, error) {
	name, err := t.name(tenant, purpose)
	if err != nil {
		return "", err
	}
	if identifier == "" {
		return "", nil
	}
	var out struct {
		HMAC string `json:"hmac"`
	}
	err = t.withKey(ctx, name, func() error {
		return t.conn().call(ctx, http.MethodPost, "hmac/"+name, map[string]any{
			"input":       base64.StdEncoding.EncodeToString([]byte(identifier)),
			"algorithm":   "sha2-256",
			"key_version": transitVersion,
		}, &out)
	})
	if err != nil {
		return "", err
	}
	raw, err := transitPayload(out.HMAC)
	if err != nil {
		return "", err
	}
	return PseudonymPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Seal implements Sealer. The engine encrypts under the same key the
// pseudonym came from; it keeps the HMAC key of a version apart from its
// encryption key, so this is not one key serving two algorithms.
func (t *Transit) Seal(ctx context.Context, tenant string, purpose Purpose, plaintext []byte) ([]byte, error) {
	name, err := t.name(tenant, purpose)
	if err != nil {
		return nil, err
	}
	var out struct {
		Ciphertext string `json:"ciphertext"`
	}
	err = t.withKey(ctx, name, func() error {
		return t.conn().call(ctx, http.MethodPost, "encrypt/"+name, map[string]any{
			"plaintext":   base64.StdEncoding.EncodeToString(plaintext),
			"key_version": transitVersion,
		}, &out)
	})
	if err != nil {
		return nil, err
	}
	return []byte(out.Ciphertext), nil
}

// Open implements Sealer.
func (t *Transit) Open(ctx context.Context, tenant string, purpose Purpose, sealed []byte) ([]byte, error) {
	name, err := t.name(tenant, purpose)
	if err != nil {
		return nil, err
	}
	var out struct {
		Plaintext string `json:"plaintext"`
	}
	err = t.conn().call(ctx, http.MethodPost, "decrypt/"+name, map[string]any{
		"ciphertext": string(sealed),
	}, &out)
	if err != nil {
		// Opening never creates a key: a sealed value under a key that does
		// not exist was not sealed here.
		return nil, transitRefusal(name, err)
	}
	return base64.StdEncoding.DecodeString(out.Plaintext)
}

// Destroy implements Provider. It needs rights on the key itself that the
// writer's role must not have; it is the CLI's, run by whoever may erase.
//
// The steps are ordered so that stopping between any two leaves nothing
// usable that should not be: the first version is refused before it is
// trimmed, and trimmed before the call returns.
func (t *Transit) Destroy(ctx context.Context, tenant string, purpose Purpose) error {
	name, err := t.name(tenant, purpose)
	if err != nil {
		return err
	}
	var key transitKey
	switch err := t.conn().call(ctx, http.MethodGet, "keys/"+name, nil, &key); {
	case isNotFound(err):
		// A key never made has nothing to erase, but must never be made
		// afterwards either: make it, then destroy it, and the tombstone is
		// there for whoever asks next.
		if err := t.create(ctx, name); err != nil {
			return err
		}
	case err != nil:
		return err
	case key.destroyed():
		return nil
	}
	if key.LatestVersion <= transitVersion {
		if err := t.conn().call(ctx, http.MethodPost, "keys/"+name+"/rotate", nil, nil); err != nil {
			return err
		}
	}
	for _, step := range []struct {
		path string
		body map[string]any
	}{
		{"keys/" + name + "/config", map[string]any{
			"min_decryption_version": transitVersion + 1,
			"min_encryption_version": transitVersion + 1,
		}},
		{"keys/" + name + "/trim", map[string]any{"min_available_version": transitVersion + 1}},
	} {
		if err := t.conn().call(ctx, http.MethodPost, step.path, step.body, nil); err != nil {
			return err
		}
	}
	return nil
}

// Close implements Provider. Nothing is held.
func (t *Transit) Close() error { return nil }

// transitKey is what the engine says about a key.
type transitKey struct {
	LatestVersion       int `json:"latest_version"`
	MinAvailableVersion int `json:"min_available_version"`
	MinDecryption       int `json:"min_decryption_version"`
}

// destroyed reports whether the version everything is made under is gone.
func (k transitKey) destroyed() bool {
	return k.MinAvailableVersion > transitVersion || k.MinDecryption > transitVersion
}

// withKey runs a call that needs the key, creating the key the first time a
// tenant is seen for a purpose. Creating is idempotent in the engine, so two
// replicas meeting a new tenant at once end up with one key between them.
func (t *Transit) withKey(ctx context.Context, name string, call func() error) error {
	err := call()
	if !isNotFound(err) {
		return transitRefusal(name, err)
	}
	if err := t.create(ctx, name); err != nil {
		return err
	}
	return transitRefusal(name, call())
}

// create makes a key through the encrypt endpoint, which creates one when the
// policy grants create there. That lets a writer's policy say nothing about
// transit/keys at all — a grant on those paths would reach rotate and trim,
// which is erasure.
func (t *Transit) create(ctx context.Context, name string) error {
	return transitRefusal(name, t.conn().call(ctx, http.MethodPost, "encrypt/"+name, map[string]any{
		"plaintext": "",
		"type":      "aes256-gcm96",
	}, nil))
}

// transitRefusal turns the engine's answer about a destroyed key into
// ErrDestroyed, which is what the writer and resolve act on. The engine words
// it per operation: HMAC and decrypt say the version is too old, encrypt that
// it is less than the minimum.
func transitRefusal(name string, err error) error {
	var refusal *transitError
	if errors.As(err, &refusal) && (refusal.says("too old") || refusal.says("less than the minimum")) {
		return fmt.Errorf("%w: %s", ErrDestroyed, name)
	}
	return err
}

// isNotFound reports whether the engine said there is no such key.
func isNotFound(err error) bool {
	var refusal *transitError
	if !errors.As(err, &refusal) {
		return false
	}
	return refusal.Status == http.StatusNotFound || refusal.says("key not found")
}

// transitPayload reads the bytes out of "vault:v<N>:<base64>".
func transitPayload(value string) ([]byte, error) {
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 || parts[1] != fmt.Sprintf("v%d", transitVersion) {
		return nil, fmt.Errorf("keys: transit returned a value it did not explain: %q", value)
	}
	return base64.StdEncoding.DecodeString(parts[2])
}
