package auditpulumi

import (
	"fmt"
	"regexp"
	"strings"
)

// principalRE is the form a sender or redriver takes in the queue policy: the ARN
// of an IAM role or user, with its path. The condition on the deny reads
// aws:PrincipalArn, which for a caller assuming a role is the ROLE's ARN and never
// the session's, so a session ARN, an STS ARN or an account id would be allowed
// by the policy's allow and then refused by its deny, or the reverse.
var principalRE = regexp.MustCompile(`^arn:` + `aws[a-z-]*:iam::[0-9]{12}:(role|user)/[A-Za-z0-9+=,.@_/-]+$`)

var bareAccountRE = regexp.MustCompile(`^[0-9]{12}$`)

// checkPrincipals refuses the forms that do not mean what a person expects:
// assumed-role session ARNs, `sts` ARNs, a bare 12-digit account id, anything with
// a wildcard, and anything that is not a role or user ARN. It runs on the resolved
// values, because the senders may be outputs of resources in the same stack.
func checkPrincipals(who []string) error {
	for _, p := range who {
		switch {
		case strings.Contains(p, ":assumed-role/"), strings.Contains(p, ":sts::"):
			return fmt.Errorf("auditpulumi: Ingest.Senders/Redrivers has %q, which is a session ARN: name the role's ARN "+
				"(an arn, iam, <account>, role/<path>/<name>), which is what aws:PrincipalArn carries for a role session", p)
		case bareAccountRE.MatchString(p):
			return fmt.Errorf("auditpulumi: Ingest.Senders/Redrivers has the bare account id %q: it would let every principal of "+
				"that account send; name the roles", p)
		case strings.ContainsAny(p, "*?"):
			return fmt.Errorf("auditpulumi: Ingest.Senders/Redrivers has %q: no wildcards", p)
		case !principalRE.MatchString(p):
			return fmt.Errorf("auditpulumi: Ingest.Senders/Redrivers has %q, which is not the ARN of an IAM role or user", p)
		}
	}
	return nil
}
