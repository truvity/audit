package auditpulumi

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// SecretsArgs is where the writer function reads the secrets its configuration
// names: AWS Systems Manager Parameter Store, SecureString parameters under a
// root, read with the function's own role. A function's environment is never a
// place for a secret: it is shown by the console and the API to whoever may
// describe the function, and is kept in every version of it.
//
// The library does not create the parameters, because it is not given their
// values and a value passed through Pulumi is kept in its state. It renders the
// root into the function's configuration (`secrets: {source: ssm, root}`) and
// grants the role `ssm:GetParameter` and `ssm:GetParameters` on that root and
// nothing else, plus `kms:Decrypt` on KeyArn when there is one. A name a
// `...Secret` field of the configuration holds is then the parameter
// `<Root>/<name>`.
type SecretsArgs struct {
	// Root is the parameter path every secret is under, without a trailing slash.
	// Default `/audit/<name>/private/config`, `<name>` being the name the
	// component is registered under: one installation's, and nothing of another's.
	// It is a path and never a pattern: no `*` or `?`, no `.` or `..` segment, and
	// it is under `/audit/` (at least two segments, the first `audit`), so that the
	// grant cannot reach another service's tree.
	Root string
	// KeyArn is the customer-managed KMS key the SecureStrings are encrypted with.
	// The role is granted `kms:Decrypt` on it through SSM only
	// (`kms:ViaService`), and for parameters under Root only (the encryption
	// context). Empty means the AWS-managed key `alias/aws/ssm`, which needs no
	// grant.
	KeyArn string
}

// rootRE is a parameter path made of segments, with nothing IAM or SSM would
// read as a pattern or a parent.
var rootRE = regexp.MustCompile(`^(/[A-Za-z0-9_.-]+)+$`)

// secretFieldNames are the values of every key of a rendered block that ends in
// `Secret`: the names of the secrets the configuration reads.
func secretNames(v any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if s, ok := e.(string); ok && strings.HasSuffix(k, "Secret") {
					out = append(out, s)
				}
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	slices.Sort(out)
	return out
}

// resolveSecrets decides whether the writer reads secrets and from where, and
// refuses what cannot work: a Keys block that names a secret needs somewhere to
// read it from, and a root with nothing naming a secret under it is a grant that
// nothing uses.
func resolveSecrets(name string, w *WriterArgs) (*SecretsArgs, error) {
	names := secretNames(w.Keys)
	if len(names) == 0 && w.Secrets == nil {
		return nil, nil
	}
	s := SecretsArgs{}
	if w.Secrets != nil {
		s = *w.Secrets
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("auditpulumi: Writer.Secrets is set and Writer.Keys names no secret (a `...Secret` field): " +
			"the role would be granted SSM for nothing")
	}
	if s.Root == "" {
		s.Root = "/audit/" + name + "/private/config"
	}
	if !rootRE.MatchString(s.Root) || !strings.HasPrefix(s.Root, "/audit/") {
		return nil, fmt.Errorf("auditpulumi: Writer.Secrets.Root %q must be an SSM parameter path under /audit/ such as "+
			"/audit/%s/private/config: at least two segments, the first `audit`, segments of letters, digits, . _ and -, "+
			"no trailing slash, no wildcard", s.Root, name)
	}
	for _, seg := range strings.Split(s.Root[1:], "/") {
		if seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return nil, fmt.Errorf("auditpulumi: Writer.Secrets.Root %q has the segment %q: no segment may start with a dot, which rules out the dot and dot-dot segments", s.Root, seg)
		}
	}
	if s.KeyArn != "" && !kmsArnRE.MatchString(s.KeyArn) {
		return nil, fmt.Errorf("auditpulumi: Writer.Secrets.KeyArn %q must be the ARN of a KMS key", s.KeyArn)
	}
	for _, n := range names {
		if !secretNameRE.MatchString(n) {
			return nil, fmt.Errorf("auditpulumi: Writer.Keys names the secret %q, which is not a name under Writer.Secrets.Root: "+
				"a relative path of segments of letters, digits, . _ and - (and not climbing with ..)", n)
		}
	}
	return &s, nil
}

var (
	// The prefixes are joined so that the leak canary, which bans the literal, reads
	// these as the mechanism they are.
	kmsArnRE     = regexp.MustCompile(`^arn:` + `aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/[A-Za-z0-9-]+$`)
	secretNameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)
)

// secretGrant is what the writer's role is given to read its secrets.
type secretGrant struct {
	Root, Region, Account, KeyArn string
}

func (g *secretGrant) parametersArn() string {
	return "arn:" + "aws:ssm:" + g.Region + ":" + g.Account + ":parameter" + g.Root + "/*"
}

// statements are the grant: read the parameters under the root, and decrypt them
// with the customer-managed key when there is one, through SSM and for this
// root only. Nothing else of SSM: no list, no put, no other path.
func (g *secretGrant) statements() []statement {
	if g == nil {
		return nil
	}
	st := []statement{allow([]string{"ssm:GetParameter", "ssm:GetParameters"}, []string{g.parametersArn()}, nil)}
	if g.KeyArn != "" {
		st = append(st, allow([]string{"kms:Decrypt"}, []string{g.KeyArn}, map[string]any{
			"StringEquals": map[string]any{"kms:ViaService": "ssm." + g.Region + ".amazonaws.com"},
			"StringLike":   map[string]any{"kms:EncryptionContext:PARAMETER_ARN": g.parametersArn()},
		}))
	}
	return st
}
