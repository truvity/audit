package cli

import (
	"flag"

	"github.com/truvity/audit/keys"
)

// OpenBAOFlags are how a command reaches an OpenBAO transit engine: where it
// is, which namespace, which chain its certificate comes from, and how to sign
// in. The key provider and the digest signer share them, so that the writer's
// keys and the digest's signature cannot be reached two different ways.
type OpenBAOFlags struct {
	Address   *string
	Mount     *string
	Namespace *string
	CAFile    *string
	TokenFile *string
	AuthMount *string
	AuthRole  *string
	JWTFile   *string
}

// NewOpenBAOFlags registers the connection flags. Lookup gives each default
// from the environment; nil means none. An operator's shell names the server
// the way the OpenBAO CLI does, so its variables are the fallback.
func NewOpenBAOFlags(fs *flag.FlagSet, lookup func(name, fallback string) string) *OpenBAOFlags {
	if lookup == nil {
		lookup = func(_, fallback string) string { return fallback }
	}
	return &OpenBAOFlags{
		Address: fs.String("transit-address", lookup("AUDIT_TRANSIT_ADDRESS", firstEnv("BAO_ADDR", "VAULT_ADDR")),
			"transit: the OpenBAO server, e.g. https://openbao.example:8200"),
		Mount: fs.String("transit-mount", lookup("AUDIT_TRANSIT_MOUNT", "transit"), "transit: where the engine is mounted"),
		Namespace: fs.String("transit-namespace", lookup("AUDIT_TRANSIT_NAMESPACE", firstEnv("BAO_NAMESPACE", "VAULT_NAMESPACE")),
			"transit: the OpenBAO namespace the engine and the auth mount are in; empty is root"),
		CAFile: fs.String("transit-ca-file", lookup("AUDIT_TRANSIT_CA_FILE", firstEnv("BAO_CACERT", "VAULT_CACERT")),
			"transit: a PEM bundle trusted beside the system roots, for a server on a private chain"),
		TokenFile: fs.String("transit-token-file", lookup("AUDIT_TRANSIT_TOKEN_FILE", ""),
			"transit: a file holding a token, read on every call"),
		AuthMount: fs.String("transit-auth-mount", lookup("AUDIT_TRANSIT_AUTH_MOUNT", ""),
			"transit: a JWT auth mount to sign in on with --transit-jwt-file, e.g. jwt-devel"),
		AuthRole: fs.String("transit-auth-role", lookup("AUDIT_TRANSIT_AUTH_ROLE", ""), "transit: the role on that mount"),
		JWTFile: fs.String("transit-jwt-file", lookup("AUDIT_TRANSIT_JWT_FILE", ""),
			"transit: the JWT to sign in with, normally the pod's projected service-account token"),
	}
}

// Credentials are the one way to authenticate these flags name: a JWT login
// when an auth mount is given, else a token file, else BAO_TOKEN from the
// environment.
func (o *OpenBAOFlags) Credentials() (login *keys.JWTLogin, token, tokenFile string) {
	switch {
	case *o.AuthMount != "":
		return &keys.JWTLogin{Mount: *o.AuthMount, Role: *o.AuthRole, TokenFile: *o.JWTFile}, "", ""
	case *o.TokenFile != "":
		return nil, "", *o.TokenFile
	default:
		return nil, firstEnv("BAO_TOKEN", "VAULT_TOKEN"), ""
	}
}
