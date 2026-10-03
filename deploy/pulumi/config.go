package auditpulumi

import (
	"fmt"

	yaml "go.yaml.in/yaml/v3"
)

// Where the package puts what the functions read. /var/task is the root of a
// function's package, and the binaries' own default for the configuration file.
const (
	packageRoot = "/var/task"
	// The name is joined so that a scan for emitted action names, which reads a
	// string of this shape as one, does not take it for an action.
	configFile        = "audit" + ".yaml"
	deploymentFile    = "deployment.yaml"
	cataloguesDir     = "catalogues"
	writerService     = "audit-writer"
	notaryService     = "audit-notary"
	extensionLoopback = "http://127.0.0.1:4318"
)

// archiveConfig is the `archive` block of both functions: the bucket the library
// created, the lock mode it was created with, and the key objects are encrypted
// with, which is named by alias so that the file is known before the key exists.
func archiveConfig(name, bucket, lockMode string) map[string]any {
	return map[string]any{
		"bucket":   map[string]any{"name": bucket},
		"lockMode": lowerMode(lockMode),
		"kmsKey":   archiveKeyAlias(name),
	}
}

func lowerMode(m string) string {
	switch m {
	case Compliance:
		return "compliance"
	case None:
		return "none"
	}
	return "governance"
}

func archiveKeyAlias(name string) string { return "alias/" + name + "-archive" }
func sealKeyAlias(name string) string    { return "alias/" + name + "-seal" }
func dedupeTable(name string) string     { return name + "-dedupe" }

// writerConfig is audit-writer-lambda's configuration file
// (schemas/config/audit-writer-lambda.schema.json). It holds no secret and
// nothing that is known only after something is created, so it is rendered
// before the first resource exists and ships in the function's package.
func writerConfig(name string, a *Args) ([]byte, error) {
	dyn := map[string]any{"table": dedupeTable(name)}
	if a.Writer.DedupeWindow != "" {
		dyn["window"] = a.Writer.DedupeWindow
	}
	doc := map[string]any{
		"deployment": packageRoot + "/" + deploymentFile,
		"archive":    archiveConfig(name, a.Archive.BucketName, a.Archive.ObjectLockMode),
		"dedupe":     map[string]any{"dynamodb": dyn},
		"require":    "archived",
	}
	if len(a.Writer.Catalogues) > 0 {
		doc["catalogues"] = packageRoot + "/" + cataloguesDir
	}
	if a.Writer.Keys != nil {
		doc["keys"] = a.Writer.Keys
	}
	if a.Writer.ForgetIdentities {
		doc["forgetIdentities"] = true
	}
	return render(doc)
}

// notaryConfig is audit-notary's configuration file, as the Lambda reads it: the
// same schema as the Job's, with `signer.kms` naming the seal key by alias.
func notaryConfig(name string, a *Args) ([]byte, error) {
	doc := map[string]any{
		"archive": archiveConfig(name, a.Archive.BucketName, a.Archive.ObjectLockMode),
		"signer":  map[string]any{"kms": map[string]any{"key": sealKeyAlias(name)}},
		"settle":  a.Notary.Settle,
	}
	if len(a.Notary.Profiles) > 0 {
		doc["profiles"] = a.Notary.Profiles
	}
	return render(doc)
}

func render(doc map[string]any) ([]byte, error) {
	b, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("auditpulumi: rendering a function's configuration: %w", err)
	}
	return b, nil
}

// telemetryEnv is the environment of a function with the OTLP extension: the
// extension's own settings (ACCESS_ROSTER_*, see access-roster's
// docs/integrations/aws-lambda.md) and the SDK's, which points at the
// extension's loopback proxy. No secret: the extension trades the function
// role's identity for a token.
func telemetryEnv(t *TelemetryArgs, service string) map[string]string {
	if t == nil {
		return nil
	}
	env := map[string]string{
		"ACCESS_ROSTER_ISSUER":        t.IssuerURL,
		"ACCESS_ROSTER_AUDIENCE":      t.STSAudience,
		"ACCESS_ROSTER_OTLP_ENDPOINT": t.OTLPEndpoint,
		// The exchange's audience and client id.
		"ACCESS_ROSTER_OTLP_AUDIENCE": t.OTLPAudience,
		// The SDK exports to the extension, which holds the credential.
		"OTEL_EXPORTER_OTLP_ENDPOINT": extensionLoopback,
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_SERVICE_NAME":           service,
	}
	for k, v := range t.ExtraEnv {
		env[k] = v
	}
	return env
}
