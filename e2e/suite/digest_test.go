// This file proves the digest and verify CronJobs' MECHANICS — the RBAC,
// the signing key mount, the archive credentials — by running each once
// through the Kubernetes API, the same way an operator would with `kubectl
// create job --from`. Two things it deliberately does NOT prove, and why:
//
//   - that a record this suite just wrote is IN a sealed digest. Digest
//     seals only a closed hour (internal/cli/digest.go: "a zero To means
//     the last hour that has closed"), so proving that would mean waiting
//     for a real hour boundary — a property of wall-clock time, not of
//     this box, and out of reach for a single CI run.
//   - that the archive is tamper-evident under Object Lock. This tier
//     installs with `lockMode: none` (see charts/audit/testdata/values/e2e.yaml)
//     because LocalStack Community's Object Lock support is partial; a
//     bucket with a real Object Lock is what docs/guides/testing.md asks a
//     deployer to prove that property against.
//
// The signing and chain logic itself is proved without a cluster at all,
// by internal/writer's chain tests and `just conformance`.
package suite

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"
)

// runToCompletion creates a one-off Job from a CronJob's own template —
// `kubectl create job --from=cronjob/<name>`, the same command an operator
// runs to fire a scheduled job early — and waits for it to finish. It never
// execs into a Pod: everything it asserts comes from the Job's own status,
// over the Kubernetes API.
func runToCompletion(ctx context.Context, t *testing.T, cronjob string) {
	t.Helper()

	kctx := getenv(envKubecontext, defaultKubecontext)
	jobName := fmt.Sprintf("%s-e2e-%d", cronjob, time.Now().UnixNano())

	create := exec.CommandContext(ctx, "kubectl", //nolint:gosec // fixed argv, no shell
		"--context", kctx, "-n", shared.names.Namespace,
		"create", "job", jobName, "--from=cronjob/"+cronjob,
	)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create job from cronjob/%s: %v\n%s", cronjob, err, out)
	}

	wait := exec.CommandContext(ctx, "kubectl", //nolint:gosec // fixed argv, no shell
		"--context", kctx, "-n", shared.names.Namespace,
		"wait", "job/"+jobName, "--for=condition=complete", "--timeout=120s",
	)
	out, err := wait.CombinedOutput()
	if err != nil {
		logs, _ := exec.CommandContext(ctx, "kubectl", //nolint:gosec // fixed argv, no shell
			"--context", kctx, "-n", shared.names.Namespace,
			"logs", "job/"+jobName, "--all-containers",
		).CombinedOutput()
		t.Fatalf("job/%s did not complete: %v\n%s\n--- logs ---\n%s", jobName, err, out, logs)
	}
}

// TestDigestJobRunsAndSignsWithTheMountedKey proves the digest CronJob's
// own wiring: the service account, the S3 credentials, and the signing key
// this suite's fixture generated and mounted are enough for the job to run
// to completion. See this file's header for what it does not prove.
func TestDigestJobRunsAndSignsWithTheMountedKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	runToCompletion(ctx, t, shared.names.Release+"-digest")
}

// TestVerifyJobRunsWithTheMountedPublicKey is the same proof for the verify
// CronJob's own wiring — its service account, its read-only archive
// credentials, and the public half of the same key.
func TestVerifyJobRunsWithTheMountedPublicKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	runToCompletion(ctx, t, shared.names.Release+"-verify-security")
}
