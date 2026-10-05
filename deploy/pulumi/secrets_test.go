package auditpulumi_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	policyconfig "github.com/truvity/policy/config"
	yaml "go.yaml.in/yaml/v3"

	auditpulumi "github.com/truvity/audit/deploy/pulumi"
)

func transitKeys(secret string) map[string]any {
	return map[string]any{"provider": "transit", "transit": map[string]any{"openbao": map[string]any{
		"address": "https://bao.example.test", "tokenSecret": secret,
	}}}
}

// Secrets never reach a function's environment: whatever the configuration names,
// every function the library makes has the path of the file and the telemetry's
// own settings, and nothing that looks like a credential, by key or by value.
func TestNoSecretIsInAnyFunctionEnvironment(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.Writer.Keys = transitKeys("openbao/token") })
	if err != nil {
		t.Fatal(err)
	}
	keyLooksSecret := regexp.MustCompile(`(?i)(token|secret|password|passwd|private|credential|headers|authorization|api[_-]?key)`)
	valueLooksSecret := regexp.MustCompile(`(?i)(^bearer\s|^basic\s|authorization\s*=|://[^/\s:@]+:[^/\s@]+@|ssm:|^s\.[A-Za-z0-9]{8,}|^eyJ)`)
	fns := rec.ofType("aws:lambda/function:Function")
	if len(fns) < 2 {
		t.Fatalf("only %d functions: the writer and the notary are both made", len(fns))
	}
	for _, f := range fns {
		for k, v := range variables(t, f) {
			ok := k == "AUDIT_CONFIG" || k == "AUDIT_CONFIG_LAYER"
			for _, prefix := range []string{"ACCESS_ROSTER_", "AUDIT_OTLP_", "OTEL_"} {
				ok = ok || strings.HasPrefix(k, prefix)
			}
			if !ok || keyLooksSecret.MatchString(k) || valueLooksSecret.MatchString(v) {
				t.Errorf("%s: environment variable %s is not one the library sets, or looks like a credential", f.Name, k)
			}
		}
	}
}

func TestTelemetryExtraEnvCannotCarryACredential(t *testing.T) {
	for _, k := range []string{"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_X_TOKEN", "OTEL_X_SECRET"} {
		_, _, err := build(t, func(a *auditpulumi.Args) { a.Telemetry.ExtraEnv = map[string]string{k: "x"} })
		if err == nil || !strings.Contains(err.Error(), "credential") {
			t.Errorf("%s accepted: %v", k, err)
		}
	}
}

// What the configuration names is read from SSM under a root, and the role gets
// that root and nothing else of SSM.
func TestTheWriterReadsItsSecretsFromSSMUnderItsOwnRoot(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.Writer.Keys = transitKeys("openbao/token") })
	if err != nil {
		t.Fatal(err)
	}
	body := layerFiles(t, rec, "audit-writer")["audit.yaml"]
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["apiVersion"] != "audit.truvity.github.io/audit-writer-lambda/v2" {
		t.Errorf("apiVersion = %v", doc["apiVersion"])
	}
	sec, _ := doc["secrets"].(map[string]any)
	if sec["source"] != "ssm" || sec["root"] != "/audit/audit/private/config" {
		t.Errorf("secrets = %v", doc["secrets"])
	}
	// The same file is what the binary's schema accepts.
	raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "config", "audit-writer-lambda.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := policyconfig.Validate(any(doc), raw); err != nil {
		t.Errorf("the configuration does not validate: %v\n%s", err, body)
	}
	// What the layer holds is the name of the secret, which is not one.
	if !strings.Contains(body, "tokenSecret: openbao/token") {
		t.Errorf("the layer does not name the secret:\n%s", body)
	}

	g := grants(policy(t, rec, "audit-writer"))
	want := []string{arnp + "ssm:eu-west-1:" + account + ":parameter/audit/audit/private/config/*"}
	for _, act := range []string{"ssm:GetParameter", "ssm:GetParameters"} {
		if got := g[act]; len(got) != 1 || got[0] != want[0] {
			t.Errorf("%s on %v, want %v", act, got, want)
		}
	}
	for act := range g {
		if strings.HasPrefix(act, "ssm:") && act != "ssm:GetParameter" && act != "ssm:GetParameters" {
			t.Errorf("the writer is granted %s", act)
		}
	}
	// No customer key was given, so there is no decrypt grant for SSM to use.
	for _, s := range policy(t, rec, "audit-writer") {
		if c, _ := s["Condition"].(map[string]any); c != nil {
			if _, viaSSM := c["StringEquals"].(map[string]any)["kms:ViaService"]; viaSSM {
				t.Errorf("a decrypt grant through SSM without a key: %v", s)
			}
		}
	}
	// Nobody but the writer reads them.
	for _, role := range []string{"audit-notary", "audit-observe-reader"} {
		for _, rp := range rec.ofType("aws:iam/rolePolicy:RolePolicy") {
			if rp.Name == role && strings.Contains(rp.Inputs["policy"].StringValue(), "ssm:") {
				t.Errorf("%s may read SSM", role)
			}
		}
	}
}

func TestACustomerKeyIsGrantedThroughSSMForTheRootOnly(t *testing.T) {
	key := arnp + "kms:eu-west-1:" + account + ":key/1234abcd-12ab-34cd-56ef-1234567890ab"
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.Keys = transitKeys("openbao/token")
		a.Writer.Secrets = &auditpulumi.SecretsArgs{Root: "/audit/acme/private", KeyArn: key}
		a.Region = "eu-west-1"
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range policy(t, rec, "audit-writer") {
		acts := strs(s["Action"])
		if len(acts) != 1 || acts[0] != "kms:Decrypt" || strs(s["Resource"])[0] != key {
			continue
		}
		c := s["Condition"].(map[string]any)
		via := c["StringEquals"].(map[string]any)["kms:ViaService"]
		ctx := c["StringLike"].(map[string]any)["kms:EncryptionContext:PARAMETER_ARN"]
		if via == "ssm.eu-west-1.amazonaws.com" && ctx == arnp+"ssm:eu-west-1:"+account+":parameter/audit/acme/private/*" {
			found = true
		}
	}
	if !found {
		t.Errorf("the key is not granted through SSM for the root: %v", policy(t, rec, "audit-writer"))
	}
}

// No secret named, no SSM: the grant is for what the configuration uses.
func TestTheWriterIsGrantedNoSSMWhenItNamesNoSecret(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	for act := range grants(policy(t, rec, "audit-writer")) {
		if strings.HasPrefix(act, "ssm:") {
			t.Errorf("the writer is granted %s", act)
		}
	}
	if strings.Contains(layerFiles(t, rec, "audit-writer")["audit.yaml"], "secrets:") {
		t.Error("a secrets block was rendered for a configuration that names none")
	}
}

func TestASecretsRootThatIsAPatternOrLeavesItsPlaceIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		keys map[string]any
		sec  *auditpulumi.SecretsArgs
		want string
	}{
		"a wildcard":           {transitKeys("t"), &auditpulumi.SecretsArgs{Root: "/audit/*"}, "Root"},
		"a question mark":      {transitKeys("t"), &auditpulumi.SecretsArgs{Root: "/audit/?"}, "Root"},
		"a trailing slash":     {transitKeys("t"), &auditpulumi.SecretsArgs{Root: "/audit/x/"}, "Root"},
		"a relative root":      {transitKeys("t"), &auditpulumi.SecretsArgs{Root: "audit/x"}, "Root"},
		"outside /audit":       {transitKeys("t"), &auditpulumi.SecretsArgs{Root: "/other/tree"}, "under /audit/"},
		"only /audit":          {transitKeys("t"), &auditpulumi.SecretsArgs{Root: "/audit"}, "under /audit/"},
		"a dot segment":        {transitKeys("t"), &auditpulumi.SecretsArgs{Root: "/audit/./x"}, "Root"},
		"the root of all":      {transitKeys("t"), &auditpulumi.SecretsArgs{Root: "/"}, "Root"},
		"a parent":             {transitKeys("t"), &auditpulumi.SecretsArgs{Root: "/audit/../x"}, "Root"},
		"a bad key arn":        {transitKeys("t"), &auditpulumi.SecretsArgs{KeyArn: "alias/aws/ssm"}, "KeyArn"},
		"a name that climbs":   {transitKeys("../other/token"), nil, "not a name under"},
		"a name from the root": {transitKeys("/other/token"), nil, "not a name under"},
		"a grant for nothing":  {nil, &auditpulumi.SecretsArgs{}, "names no secret"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := build(t, func(a *auditpulumi.Args) { a.Writer.Keys, a.Writer.Secrets = c.keys, c.sec })
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("want a refusal naming %q, got %v", c.want, err)
			}
		})
	}
}
