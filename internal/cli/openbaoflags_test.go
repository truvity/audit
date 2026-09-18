package cli

import (
	"flag"
	"testing"
)

// The flags name one way to sign in, in the order a deployment means them: a
// JWT login when an auth mount is given, else a token file, else BAO_TOKEN.
func TestOpenBAOCredentialsPickOneWay(t *testing.T) {
	t.Setenv("BAO_TOKEN", "from-the-shell")
	t.Setenv("VAULT_TOKEN", "")
	for name, c := range map[string]struct {
		args            []string
		login           bool
		token, tokenFor string
	}{
		"a JWT login": {args: []string{
			"--transit-auth-mount=jwt-devel", "--transit-auth-role=audit-writer",
			"--transit-jwt-file=/var/run/openbao/token", "--transit-token-file=/ignored",
		}, login: true},
		"a token file":         {args: []string{"--transit-token-file=/etc/audit/transit/token"}, tokenFor: "/etc/audit/transit/token"},
		"the operator's shell": {token: "from-the-shell"},
	} {
		t.Run(name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			o := NewOpenBAOFlags(fs, nil)
			if err := fs.Parse(c.args); err != nil {
				t.Fatal(err)
			}
			login, token, file := o.Credentials()
			if (login != nil) != c.login || token != c.token || file != c.tokenFor {
				t.Fatalf("login=%v token=%q file=%q", login, token, file)
			}
			if login != nil && (login.Mount != "jwt-devel" || login.Role != "audit-writer" ||
				login.TokenFile != "/var/run/openbao/token") {
				t.Fatalf("login %+v", *login)
			}
		})
	}
}
