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
	OpenBAO *OpenBAOFlags
	Prefix  *string
}

// NewKeyFlags registers the key flags on a flag set. Lookup gives each flag's
// default from the environment; nil means no environment.
func NewKeyFlags(fs *flag.FlagSet, lookup func(name, fallback string) string) *KeyFlags {
	if lookup == nil {
		lookup = func(_, fallback string) string { return fallback }
	}
	return &KeyFlags{
		Provider: fs.String("key-provider", lookup("AUDIT_KEY_PROVIDER", "none"),
			"where pseudonymisation keys live: none (the default: no pseudonyms, no resolve), "+
				"local (a root and a directory) or transit (OpenBAO)"),
		Root: fs.String("key-root", lookup("AUDIT_KEY_ROOT", ""),
			"local: file holding the 32-byte root the data keys are wrapped under"),
		Dir:     fs.String("key-dir", lookup("AUDIT_KEY_DIR", ""), "local: where wrapped data keys are kept"),
		OpenBAO: NewOpenBAOFlags(fs, lookup),
		Prefix:  fs.String("transit-prefix", lookup("AUDIT_TRANSIT_PREFIX", "audit"), "transit: what every key's name starts with"),
	}
}

// Configured reports whether the flags name a provider at all. The query
// service offers resolve only when they do.
func (k *KeyFlags) Configured() bool {
	switch *k.Provider {
	case "none", "":
		return false
	case "transit":
		return *k.OpenBAO.Address != ""
	default:
		return *k.Root != ""
	}
}

// Local reports whether the provider is the local one, whose directory is the
// only copy of the keys and so carries rules the transit provider does not.
func (k *KeyFlags) Local() bool { return *k.Provider == "local" }

// Open builds the provider, which is nil where a deployment runs without one.
// A nil provider is the default: staff are kept in clear because that is what
// accountability is for, and people outside arrive as identifiers the
// application already minted. A profile that asks for a pseudonym anyway is
// refused by the writer, naming the profile — see
// docs/decisions/0013-no-pseudonymisation-keys-by-default.md.
func (k *KeyFlags) Open(ctx context.Context) (keys.Provider, error) {
	switch *k.Provider {
	case "none", "":
		return nil, nil
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
		o := k.OpenBAO
		if *o.Address == "" {
			return nil, errors.New("the transit key provider needs --transit-address")
		}
		login, token, tokenFile := o.Credentials()
		return keys.NewTransit(ctx, &keys.Transit{
			Address: *o.Address, Mount: *o.Mount, Namespace: *o.Namespace, CAFile: *o.CAFile,
			Prefix: *k.Prefix, Login: login, Token: token, TokenFile: tokenFile,
		})
	default:
		return nil, fmt.Errorf("--key-provider is none, local or transit, not %q", *k.Provider)
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
