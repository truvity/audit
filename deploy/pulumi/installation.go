package auditpulumi

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// The Lambda functions' build, which a telemetry layer has to match.
const (
	// LambdaArchitecture is the architecture of the library's functions.
	LambdaArchitecture = "arm64"
	// LambdaRuntime is their runtime.
	LambdaRuntime = "provided.al2023"
)

// Component is one workload of an installation deployed from the chart that holds
// an AWS role of its own. The others (receiver, purge, clock-sync, migrate) hold
// none on purpose: the receiver publishes to JetStream and holds neither the
// bucket nor a key, purge works on the index only, clock-sync records through
// the sink and migrate applies the index schema.
type Component struct {
	// Name is the chart component: writer, digest, verify or query.
	Name string
	// ServiceAccount is the chart's ServiceAccount for it, in the release the
	// chart was installed as.
	ServiceAccount string
}

// installationComponents are the components that hold a role, in a fixed order.
var installationComponents = []string{"writer", "digest", "verify", "query"}

// InstallationComponents are the AWS identities of an installation of the chart
// named release, one per component that holds a role, in a fixed order. The
// writer's ServiceAccount is the release's own; the others are suffixed with the
// component, as the chart names them.
func InstallationComponents(release string) []Component {
	out := make([]Component, len(installationComponents))

	for i, name := range installationComponents {
		sa := release
		if name != "writer" {
			sa = release + "-" + name
		}

		out[i] = Component{Name: name, ServiceAccount: sa}
	}

	return out
}

// ComponentRoleName is the IAM role name a deployment gives a component of an
// installation that runs on a cluster (Pod Identity): `<prefix>-audit-<component>`.
func ComponentRoleName(prefix, component string) string {
	return prefix + "-audit-" + component
}

// CheckName reports whether name is one the library accepts for a component: it
// is in every role, queue and function name.
func CheckName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("auditpulumi: the component name %q must be 1-32 characters of a-z, 0-9 and -, "+
			"starting with a letter: it is in every role, queue and function name", name)
	}

	return nil
}

// CheckRolePath reports whether p is an IAM path the library accepts for its roles.
func CheckRolePath(p string) error {
	if !strings.HasPrefix(p, "/") || !strings.HasSuffix(p, "/") {
		return fmt.Errorf("auditpulumi: RolePath %q must start and end with /", p)
	}

	return nil
}

// CheckLockMode reports whether mode is a lock mode of the archive bucket.
func CheckLockMode(mode string) error {
	switch mode {
	case None, Governance, Compliance:
		return nil
	}

	return fmt.Errorf("auditpulumi: Archive.ObjectLockMode %q must be NONE, GOVERNANCE or COMPLIANCE", mode)
}

// CheckProfile reports whether name can be a profile: it is the first component
// of every object key.
func CheckProfile(name string) error {
	if !keyComponent.MatchString(name) {
		return fmt.Errorf("auditpulumi: Archive.Profiles has %q, which is not a key component (no /, no leading dot)", name)
	}

	return nil
}

// ArchiveBucketName is the archive bucket's name for a prefix, the account and
// the region: a bucket name is global, so it carries both.
func ArchiveBucketName(prefix, accountID, region string) string {
	return fmt.Sprintf("%s-%s-%s", prefix, accountID, region)
}

// CheckBucketPrefix reports whether prefix makes a valid bucket name (3 to 63
// characters of a-z, 0-9 and -) with any 12-digit account and any region.
func CheckBucketPrefix(prefix string) error {
	if !bucketPrefixRE.MatchString(prefix) {
		return fmt.Errorf("auditpulumi: bucket prefix %q is not a bucket name prefix", prefix)
	}

	// A 12-digit account id and a long region name fit well inside the 63.
	if n := len(ArchiveBucketName(prefix, strings.Repeat("0", 12), "eu-central-1")); n > 63 {
		return fmt.Errorf("auditpulumi: bucket prefix %q makes a bucket name of %d characters (63 at most)", prefix, n)
	}

	return nil
}

// CheckTelemetryURLs reports whether the OIDC issuer and the OTLP endpoint of a
// Telemetry are https URLs: a bearer token crosses them.
func CheckTelemetryURLs(issuerURL, otlpEndpoint string) error {
	var errs []error

	for field, v := range map[string]string{"IssuerURL": issuerURL, "OTLPEndpoint": otlpEndpoint} {
		if u, err := url.Parse(v); err != nil || u.Scheme != "https" || u.Host == "" {
			errs = append(errs, fmt.Errorf("auditpulumi: Telemetry.%s %q must be an https URL", field, v))
		}
	}

	return errors.Join(errs...)
}

// CheckExtensionLayer reports whether an OTLP extension layer's published
// architectures and runtimes fit the library's functions: arm64 on
// provided.al2023.
func CheckExtensionLayer(architectures, runtimes []string) error {
	var errs []error

	if !slices.Contains(architectures, LambdaArchitecture) {
		errs = append(errs, fmt.Errorf("auditpulumi: the layer's architectures %v must include %s, the functions' architecture",
			architectures, LambdaArchitecture))
	}

	for _, a := range architectures {
		if a != "arm64" && a != "amd64" {
			errs = append(errs, fmt.Errorf("auditpulumi: the layer's architecture %q is not arm64 or amd64", a))
		}
	}

	if !slices.Contains(runtimes, LambdaRuntime) {
		errs = append(errs, fmt.Errorf("auditpulumi: the layer's compatible runtimes %v must include %s, the functions' runtime", runtimes, LambdaRuntime))
	}

	return errors.Join(errs...)
}
