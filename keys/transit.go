package keys

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// TransitSigner signs the digest chain with an OpenBAO (or Vault) transit key.
//
// Like the KMS signer it keeps the private half out of the archive's reach:
// the key lives in the transit engine, the digest job's policy may call
// transit/sign on it, and the writer's may not. It is the signer for a
// deployment whose secrets live in OpenBAO rather than a cloud's KMS.
//
// The key is an ed25519 transit key, which signs the message itself — so its
// signatures are checked by the same Verify as a local key, with only the
// public half this exports.
//
// A transit key can be rotated, and transit signs with the newest version
// unless told otherwise. So the signer reads the key's latest version once and
// pins every signature to it: the public half it exports and the signatures it
// makes always belong together, and KeyID names the version, so a verifier can
// tell which public half a digest wants after a rotation.
type TransitSigner struct {
	// Address is the server, e.g. https://openbao.example:8200.
	Address string
	// Mount is where the transit engine is mounted. Empty means "transit".
	Mount string
	// Key is the transit key's name.
	Key string
	// Namespace is the OpenBAO namespace the engine lives in — in the estate,
	// the environment's. Empty is the root namespace.
	Namespace string
	// CAFile is a PEM bundle trusted beside the system roots, for a server
	// whose certificate comes from a private chain.
	CAFile string
	// One way to authenticate: Login, which signs in with the pod's projected
	// service-account token and needs no stored secret; or Token; or
	// TokenFile, read on every call, for a token something else keeps renewed.
	Login     *JWTLogin
	Token     string
	TokenFile string
	HTTP      *http.Client

	state   baoState
	once    sync.Once
	version int
	public  []byte
	err     error
}

// NewTransitSigner returns a signer that has already read its key.
//
// Loading first matters: a digest names its signer before it is signed, and the
// name carries the key version, which is only known once the key has been read.
// It also means a key of the wrong type, or a token that cannot read it, stops
// the job before any window is sealed.
func NewTransitSigner(ctx context.Context, s *TransitSigner) (*TransitSigner, error) {
	if s.Address == "" || s.Key == "" {
		return nil, errors.New("keys: a transit signer needs an address and a key")
	}
	if err := s.conn().check(); err != nil {
		return nil, err
	}
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// conn is the signer's connection to the engine.
func (s *TransitSigner) conn() openbao {
	return openbao{
		Address: s.Address, Mount: s.Mount, Namespace: s.Namespace, CAFile: s.CAFile,
		Login: s.Login, Token: s.Token, TokenFile: s.TokenFile, HTTP: s.HTTP, state: &s.state,
	}
}

// call makes one request to the transit engine and decodes its data.
func (s *TransitSigner) call(ctx context.Context, method, path string, body, into any) error {
	return s.conn().call(ctx, method, path, body, into)
}

// load reads the key's type and latest version, once.
func (s *TransitSigner) load(ctx context.Context) error {
	s.once.Do(func() {
		// The versions are described differently per key type — a
		// symmetric key's are bare timestamps — so the type is checked before
		// a version is read as a signing key's.
		var key struct {
			Type          string                     `json:"type"`
			LatestVersion int                        `json:"latest_version"`
			Keys          map[string]json.RawMessage `json:"keys"`
		}
		if err := s.call(ctx, http.MethodGet, "keys/"+s.Key, nil, &key); err != nil {
			s.err = err
			return
		}
		if key.Type != "ed25519" {
			s.err = fmt.Errorf("keys: transit key %s is %s; the digest chain is signed with ed25519", s.Key, key.Type)
			return
		}
		var version struct {
			PublicKey string `json:"public_key"`
		}
		if err := json.Unmarshal(key.Keys[strconv.Itoa(key.LatestVersion)], &version); err != nil {
			s.err = fmt.Errorf("keys: transit key %s v%d: %w", s.Key, key.LatestVersion, err)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(version.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			s.err = fmt.Errorf("keys: transit key %s v%d has no ed25519 public key", s.Key, key.LatestVersion)
			return
		}
		der, err := x509.MarshalPKIXPublicKey(ed25519.PublicKey(raw))
		if err != nil {
			s.err = err
			return
		}
		s.version = key.LatestVersion
		s.public = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	})
	return s.err
}

// Sign implements Signer.
func (s *TransitSigner) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	var out struct {
		Signature string `json:"signature"`
	}
	if err := s.call(ctx, http.MethodPost, "sign/"+s.Key, map[string]any{
		"input":       base64.StdEncoding.EncodeToString(message),
		"key_version": s.version,
	}, &out); err != nil {
		return nil, err
	}
	// "vault:v<N>:<base64>", whichever server it came from.
	parts := strings.SplitN(out.Signature, ":", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("keys: transit returned a signature it did not explain: %q", out.Signature)
	}
	return base64.StdEncoding.DecodeString(parts[2])
}

// PublicKey implements Signer.
func (s *TransitSigner) PublicKey(ctx context.Context) ([]byte, error) {
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	return s.public, nil
}

// KeyID implements Signer. It names the version, so that after a rotation a
// verifier can tell which public half a digest was signed with.
func (s *TransitSigner) KeyID() string {
	if s.version == 0 {
		return "transit:" + s.Key
	}
	return fmt.Sprintf("transit:%s:v%d", s.Key, s.version)
}
