package keys

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Local keeps wrapped data keys on the local filesystem, or in memory when no
// directory is given.
//
// It is for tests, for a single-node deployment, and for the conformance suite,
// which has to run without a cloud. A deployment that must survive the loss of
// one machine wants the KMS or transit provider instead, because a key that
// exists in one place is a trail that can be made unreadable by one disk
// failing — which is not erasure, but loss.
type Local struct {
	// Root wraps the data keys. Without one a random root is generated and
	// nothing survives the process, which is fine for a test and for nothing
	// else.
	Root []byte
	// Dir holds the wrapped keys. Empty means memory only.
	Dir string

	once      sync.Once
	initErr   error
	aead      cipher.AEAD
	mu        sync.RWMutex
	cache     map[string][]byte
	destroyed map[string]bool
}

// NewLocal returns a provider wrapping its keys under root. A root of the wrong
// length is refused rather than stretched, because a silently weakened key is
// worse than none.
func NewLocal(root []byte, dir string) (*Local, error) {
	if len(root) != 0 && len(root) != 32 {
		return nil, fmt.Errorf("keys: a local root must be 32 bytes, got %d", len(root))
	}
	l := &Local{Root: root, Dir: dir}
	if err := l.init(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Local) init() error {
	l.once.Do(func() {
		l.cache = map[string][]byte{}
		l.destroyed = map[string]bool{}
		root := l.Root
		if len(root) == 0 {
			root = make([]byte, 32)
			if _, err := rand.Read(root); err != nil {
				l.initErr = fmt.Errorf("keys: %w", err)
				return
			}
		}
		block, err := aes.NewCipher(root)
		if err != nil {
			l.initErr = fmt.Errorf("keys: %w", err)
			return
		}
		if l.aead, err = cipher.NewGCM(block); err != nil {
			l.initErr = fmt.Errorf("keys: %w", err)
			return
		}
		if l.Dir != "" {
			l.initErr = os.MkdirAll(l.Dir, 0o700)
		}
	})
	return l.initErr
}

// Pseudonym implements Provider.
func (l *Local) Pseudonym(_ context.Context, tenant string, purpose Purpose, identifier string) (string, error) {
	if err := l.init(); err != nil {
		return "", err
	}
	if err := checkName(tenant, purpose); err != nil {
		return "", err
	}
	if identifier == "" {
		return "", nil
	}
	key, err := l.key(tenant, purpose)
	if err != nil {
		return "", err
	}
	return pseudonym(key, identifier), nil
}

// Seal implements Sealer.
//
// The sealing key is derived from the data key rather than being the data key:
// one key used both for pseudonyms (HMAC) and for encryption (AES-GCM) would
// be one key serving two algorithms, which is the kind of reuse that turns an
// attack on one into an attack on both.
func (l *Local) Seal(_ context.Context, tenant string, purpose Purpose, plaintext []byte) ([]byte, error) {
	aead, err := l.sealing(tenant, purpose)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	return aead.Seal(nonce, nonce, plaintext, []byte(keyName(tenant, purpose))), nil
}

// Open implements Sealer.
func (l *Local) Open(_ context.Context, tenant string, purpose Purpose, sealed []byte) ([]byte, error) {
	aead, err := l.sealing(tenant, purpose)
	if err != nil {
		return nil, err
	}
	if len(sealed) < aead.NonceSize() {
		return nil, errors.New("keys: a sealed value too short to be one")
	}
	nonce, body := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, body, []byte(keyName(tenant, purpose)))
	if err != nil {
		return nil, fmt.Errorf("keys: the sealed value does not open under %s: %w", keyName(tenant, purpose), err)
	}
	return plain, nil
}

// sealing is the AEAD derived from a tenant's data key for a purpose.
func (l *Local) sealing(tenant string, purpose Purpose) (cipher.AEAD, error) {
	if err := l.init(); err != nil {
		return nil, err
	}
	if err := checkName(tenant, purpose); err != nil {
		return nil, err
	}
	key, err := l.key(tenant, purpose)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("audit/seal/v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	return cipher.NewGCM(block)
}

// Destroy implements Provider.
func (l *Local) Destroy(_ context.Context, tenant string, purpose Purpose) error {
	if err := l.init(); err != nil {
		return err
	}
	if err := checkName(tenant, purpose); err != nil {
		return err
	}
	name := keyName(tenant, purpose)

	l.mu.Lock()
	delete(l.cache, name)
	l.destroyed[name] = true
	l.mu.Unlock()

	if l.Dir == "" {
		return nil
	}
	if err := os.Remove(l.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("keys: %w", err)
	}
	// A marker, so a restart does not mint a fresh key for a tenant whose key
	// was destroyed and hand the same person a second identity.
	if err := os.WriteFile(l.path(name)+".destroyed", nil, 0o600); err != nil {
		return fmt.Errorf("keys: %w", err)
	}
	return nil
}

// Close implements Provider.
func (l *Local) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for name, key := range l.cache {
		for i := range key {
			key[i] = 0
		}
		delete(l.cache, name)
	}
	return nil
}

// key returns the unwrapped data key, creating and wrapping one if there is
// none.
func (l *Local) key(tenant string, purpose Purpose) ([]byte, error) {
	name := keyName(tenant, purpose)

	l.mu.RLock()
	key, ok := l.cache[name]
	destroyed := l.destroyed[name]
	l.mu.RUnlock()
	switch {
	case destroyed:
		return nil, fmt.Errorf("%w: %s", ErrDestroyed, name)
	case ok:
		return key, nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if key, ok := l.cache[name]; ok {
		return key, nil
	}
	if l.destroyed[name] {
		return nil, fmt.Errorf("%w: %s", ErrDestroyed, name)
	}
	if l.Dir != "" {
		if _, err := os.Stat(l.path(name) + ".destroyed"); err == nil {
			l.destroyed[name] = true
			return nil, fmt.Errorf("%w: %s", ErrDestroyed, name)
		}
		wrapped, err := os.ReadFile(l.path(name))
		if err == nil {
			key, err := l.unwrap(wrapped)
			if err != nil {
				return nil, err
			}
			l.cache[name] = key
			return key, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("keys: %w", err)
		}
	}

	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	if l.Dir != "" {
		wrapped, err := l.wrap(key)
		if err != nil {
			return nil, err
		}
		// Two writers sharing this directory can reach here for the same
		// tenant at the same moment, each with its own random key. Whichever
		// file landed last would win on disk, but both writers would keep the
		// key they minted in memory, and the same person would carry two
		// pseudonyms until a restart. So a key is published only if none is
		// there, and a writer that loses adopts the one that won.
		won, err := createOnce(l.path(name), wrapped)
		if err != nil {
			return nil, err
		}
		if !won {
			existing, err := os.ReadFile(l.path(name))
			if err != nil {
				return nil, fmt.Errorf("keys: %w", err)
			}
			if key, err = l.unwrap(existing); err != nil {
				return nil, err
			}
		}
	}
	l.cache[name] = key
	return key, nil
}

// createOnce writes a file only if it does not exist, and reports whether this
// call is the one that created it.
//
// The body goes to a temporary file first and is then linked into place: a
// link fails if the name is taken, which makes "create unless present" one
// atomic step, and a reader never sees a half-written file under the real
// name, as it could between an exclusive create and the write that follows.
func createOnce(path string, body []byte) (bool, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if err != nil {
		return false, fmt.Errorf("keys: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("keys: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("keys: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("keys: %w", err)
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("keys: %w", err)
	}
	return true, nil
}

// DirectoryID is this key directory's identity, minted the first time any
// writer asks and read back ever after.
//
// Data keys are random, so a directory is not interchangeable with another one
// holding the same root: two writers that do not share one mint different keys
// for the same tenant, and a directory lost and recreated re-keys every tenant.
// Neither is visible from inside one writer. The identity is what lets a
// shared store notice: every writer registers it there, and one that brings a
// different directory is refused. See postgres.BindKeyDirectory.
func (l *Local) DirectoryID() (string, error) {
	if err := l.init(); err != nil {
		return "", err
	}
	if l.Dir == "" {
		return "", errors.New("keys: a memory-only provider has no directory to identify")
	}
	path := filepath.Join(l.Dir, ".directory-id")
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("keys: %w", err)
	}
	if _, err := createOnce(path, []byte(fmt.Sprintf("%x\n", raw))); err != nil {
		return "", err
	}
	id, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("keys: %w", err)
	}
	return strings.TrimSpace(string(id)), nil
}

func (l *Local) wrap(key []byte) ([]byte, error) {
	nonce := make([]byte, l.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	return l.aead.Seal(nonce, nonce, key, nil), nil
}

func (l *Local) unwrap(wrapped []byte) ([]byte, error) {
	size := l.aead.NonceSize()
	if len(wrapped) < size {
		return nil, errors.New("keys: the wrapped key is truncated")
	}
	key, err := l.aead.Open(nil, wrapped[:size], wrapped[size:], nil)
	if err != nil {
		return nil, fmt.Errorf("keys: the wrapped key does not open under this root: %w", err)
	}
	return key, nil
}

func keyName(tenant string, purpose Purpose) string { return tenant + "." + string(purpose) }

func (l *Local) path(name string) string { return filepath.Join(l.Dir, name+".key") }
