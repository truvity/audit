package suite

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/truvity/gemaal/pkg/harness"

	"github.com/truvity/audit/e2e/fixture"
)

const (
	// envNamespace is the ONE variable that turns the suite on. Unset,
	// every test in this package is skipped — see TestMain.
	envNamespace = "E2E_NAMESPACE"
	// envRelease overrides the fixture's own release name default.
	envRelease = "E2E_RELEASE"
	// envKubecontext targets a kubeconfig context, defaulted to the local
	// box's own convention — this suite's box is truvity/policy's, never
	// created by this repository, and a second kind cluster in another
	// terminal must never silently redirect it.
	envKubecontext     = "E2E_KCTX"
	defaultKubecontext = "kind-policy"
)

// env is everything the suite resolved once, in TestMain: the tenant's
// names (from the fixture's own resolver) and the cluster this run reaches
// Services through.
type env struct {
	cluster   *harness.Cluster
	names     fixture.Names
	writerDSN string
	queryDSN  string
}

func getenv(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func namespaceFromEnv() (string, bool) {
	ns := strings.TrimSpace(os.Getenv(envNamespace))
	return ns, ns != ""
}

func resolveEnvWithTimeout(namespace string, timeout time.Duration) (env, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return resolveEnv(ctx, namespace)
}

// resolveEnv resolves everything a test needs: the fixture's names, read
// off charts/audit/testdata/values/e2e.yaml exactly as apply.sh and the
// chart install do, and the two roles' connection strings —
// e2e/fixture/apply.sh wrote them into Secrets, which this package has no
// other way to learn.
func resolveEnv(ctx context.Context, namespace string) (env, error) {
	d := fixture.DefaultOptions()

	names, err := fixture.Resolve(fixture.Options{
		Namespace: namespace,
		Release:   getenv(envRelease, d.Release),
	})
	if err != nil {
		return env{}, fmt.Errorf("resolve the fixture's names: %w", err)
	}

	kctx := getenv(envKubecontext, defaultKubecontext)
	cluster := &harness.Cluster{Kubecontext: kctx}

	writerDSN, err := secretURL(ctx, kctx, names.Namespace, names.WriterSecret)
	if err != nil {
		return env{}, fmt.Errorf("the writer role's connection string (%s): %w", names.WriterSecret, err)
	}
	queryDSN, err := secretURL(ctx, kctx, names.Namespace, names.QuerySecret)
	if err != nil {
		return env{}, fmt.Errorf("the query role's connection string (%s): %w", names.QuerySecret, err)
	}

	return env{
		cluster:   cluster,
		names:     names,
		writerDSN: writerDSN,
		queryDSN:  queryDSN,
	}, nil
}

// secretURL reads a Secret's "url" field, decoded. Configuration, read the
// same way e2e/fixture/apply.sh itself reads it back — not a probe of the
// product, which is why it is kubectl rather than a Service: nothing here
// serves its own database credentials over a Service, on purpose.
func secretURL(ctx context.Context, kubecontext, namespace, secret string) (string, error) {
	out, err := exec.CommandContext(ctx, "kubectl", //nolint:gosec // fixed argv, no shell
		"--context", kubecontext, "-n", namespace,
		"get", "secret", secret, "-o", "jsonpath={.data.url}",
	).Output()
	if err != nil {
		return "", fmt.Errorf("kubectl get secret %s/%s: %w", namespace, secret, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return "", fmt.Errorf("secret %s/%s: url is not valid base64: %w", namespace, secret, err)
	}
	return string(decoded), nil
}
