package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/truvity/audit/keys"
)

// KeyFlags are the flags every binary names its key provider with, so that
// the writer, the query service's resolve and `audit key destroy` cannot be
// pointed at the same keys in three different spellings.
type KeyFlags struct {
	Provider *string
	// local
	Root *string
	Dir  *string
	// transit
	Address   *string
	Mount     *string
	Prefix    *string
	TokenFile *string
}

// NewKeyFlags registers the key flags on a flag set. Lookup gives each flag's
// default from the environment; nil means no environment.
func NewKeyFlags(fs *flag.FlagSet, lookup func(name, fallback string) string) *KeyFlags {
	if lookup == nil {
		lookup = func(_, fallback string) string { return fallback }
	}
	// An operator's shell names the server the way the OpenBAO CLI does, and
	// `audit key destroy` is run from one.
	address := lookup("AUDIT_TRANSIT_ADDRESS", firstEnv("BAO_ADDR", "VAULT_ADDR"))
	return &KeyFlags{
		Provider: fs.String("key-provider", lookup("AUDIT_KEY_PROVIDER", "local"),
			"where pseudonymisation keys live: local (a root and a directory) or transit (OpenBAO)"),
		Root: fs.String("key-root", lookup("AUDIT_KEY_ROOT", ""),
			"local: file holding the 32-byte root the data keys are wrapped under"),
		Dir: fs.String("key-dir", lookup("AUDIT_KEY_DIR", ""), "local: where wrapped data keys are kept"),
		Address: fs.String("transit-address", address,
			"transit: the OpenBAO server, e.g. https://openbao.example:8200"),
		Mount:  fs.String("transit-mount", lookup("AUDIT_TRANSIT_MOUNT", "transit"), "transit: where the engine is mounted"),
		Prefix: fs.String("transit-prefix", lookup("AUDIT_TRANSIT_PREFIX", "audit"), "transit: what every key's name starts with"),
		TokenFile: fs.String("transit-token-file", lookup("AUDIT_TRANSIT_TOKEN_FILE", ""),
			"transit: a file holding the token, read on every call so an agent can renew it; "+
				"without one, BAO_TOKEN or VAULT_TOKEN"),
	}
}

// Configured reports whether the flags name a provider at all. The query
// service offers resolve only when they do.
func (k *KeyFlags) Configured() bool {
	switch *k.Provider {
	case "transit":
		return *k.Address != ""
	default:
		return *k.Root != ""
	}
}

// Local reports whether the provider is the local one, whose directory is the
// only copy of the keys and so carries rules the transit provider does not.
func (k *KeyFlags) Local() bool { return *k.Provider == "local" }

// Open builds the provider.
func (k *KeyFlags) Open(ctx context.Context) (keys.Provider, error) {
	switch *k.Provider {
	case "local":
		if *k.Root == "" {
			return nil, errors.New(
				"give the pseudonymisation root with --key-root: without keys a profile that " +
					"pseudonymises would write identifiers in clear, and this refuses to start rather than do that")
		}
		root, err := os.ReadFile(*k.Root)
		if err != nil {
			return nil, err
		}
		return keys.NewLocal(root, *k.Dir)
	case "transit":
		if *k.Address == "" {
			return nil, errors.New("the transit key provider needs --transit-address")
		}
		t := &keys.Transit{Address: *k.Address, Mount: *k.Mount, Prefix: *k.Prefix, TokenFile: *k.TokenFile}
		if t.TokenFile == "" {
			t.Token = firstEnv("BAO_TOKEN", "VAULT_TOKEN")
		}
		return keys.NewTransit(ctx, t)
	default:
		return nil, fmt.Errorf("--key-provider is local or transit, not %q", *k.Provider)
	}
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}
