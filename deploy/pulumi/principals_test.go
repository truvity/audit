package auditpulumi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	auditpulumi "github.com/truvity/audit/deploy/pulumi"
)

// An operator's redrive from the dead-letter queue is a StartMessageMoveTask that
// sends to the ingest queue as the caller, so the deny that makes the senders the
// whole of who may send would refuse it. Redrivers are added to the allow and to
// the deny's exceptions.
func TestRedriversMaySendAndAreNotDenied(t *testing.T) {
	redriver := arnp + "iam::" + account + ":role/ops/redrive"
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.Ingest.Redrivers = []pulumi.StringInput{pulumi.String(redriver)} })
	if err != nil {
		t.Fatal(err)
	}
	pol := prop(rec.one(t, "aws:sqs/queuePolicy:QueuePolicy", "audit-ingest"), "policy").StringValue()
	var doc struct{ Statement []map[string]any }
	if err := json.Unmarshal([]byte(pol), &doc); err != nil {
		t.Fatal(err)
	}
	var allowed, excepted bool
	for _, s := range doc.Statement {
		switch s["Sid"] {
		case "Senders":
			for _, p := range strs(s["Principal"].(map[string]any)["AWS"]) {
				allowed = allowed || p == redriver
			}
		case "OnlyTheSenders":
			for _, p := range strs(s["Condition"].(map[string]any)["ArnNotEquals"].(map[string]any)["aws:PrincipalArn"]) {
				excepted = excepted || p == redriver
			}
		}
	}
	if !allowed || !excepted {
		t.Errorf("the redriver is allowed %v and excepted from the deny %v: %s", allowed, excepted, pol)
	}
}

// What a sender or redriver is: the ARN of a role or user. A session, an STS ARN,
// a bare account id and a wildcard are refused on the resolved values.
func TestSendersAndRedriversMustBeRoleOrUserARNs(t *testing.T) {
	for name, bad := range map[string]string{
		"a session":      arnp + "sts::" + account + ":assumed-role/ops/alice",
		"an assumed ARN": arnp + "iam::" + account + ":assumed-role/ops/alice",
		"an account id":  account,
		"a wildcard":     arnp + "iam::" + account + ":role/*",
		"not an ARN":     "ops",
		"a root":         arnp + "iam::" + account + ":root",
	} {
		for field, set := range map[string]func(a *auditpulumi.Args, v pulumi.StringInput){
			"Senders":   func(a *auditpulumi.Args, v pulumi.StringInput) { a.Ingest.Senders = []pulumi.StringInput{v} },
			"Redrivers": func(a *auditpulumi.Args, v pulumi.StringInput) { a.Ingest.Redrivers = []pulumi.StringInput{v} },
		} {
			_, _, err := build(t, func(a *auditpulumi.Args) { set(a, pulumi.String(bad)) })
			if err == nil || !strings.Contains(err.Error(), "Ingest.Senders/Redrivers") {
				t.Errorf("%s as %s: %v", name, field, err)
			}
		}
	}
}
