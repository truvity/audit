// Package fixture stands in for the platform on the local kind box (see
// truvity/policy's hack/kind/README.md and docs/decisions/0005-kind-is-the-gate.md
// there): a Postgres database and its two roles, a JetStream stream, an S3
// bucket and the two signing keys the digest and verify jobs need — the
// things a real deployment's platform would already have provisioned before
// `helm install audit` ever runs.
//
// It reads its names from ONE file, charts/audit/testdata/values/e2e.yaml —
// the same file `helm upgrade --install` renders the chart with — rather
// than repeating them here: a value renamed in one place and not the other
// would otherwise be a silent install failure discovered only on the
// cluster. See drift_test.go, which renders that file through the chart and
// fails if the chart stops honouring a name this package gives it.
//
// The two role names and the object each secret holds are this package's
// own choice: the chart takes a Secret's NAME and reads whatever is inside
// it, and never a role or a database name directly, so there is nothing in
// the values file to agree with for those beyond the Secret and its key.
package fixture

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"sigs.k8s.io/yaml"
)

// boxPostgresAddress is the box's own Postgres server, reached the same
// cross-namespace way truvity/policy's own example fixture reaches it:
// "<service>.<namespace>.svc".
const boxPostgresAddress = "postgres.postgres.svc"

// Options are the installer's choices.
type Options struct {
	Namespace string
	Release   string
}

// DefaultOptions are what every script in this tree uses, by calling this
// rather than repeating them.
func DefaultOptions() Options {
	return Options{Namespace: "audit-e2e", Release: "audit-e2e"}
}

func (o Options) withDefaults() Options {
	d := DefaultOptions()
	if o.Namespace == "" {
		o.Namespace = d.Namespace
	}
	if o.Release == "" {
		o.Release = d.Release
	}
	return o
}

// chartValues is the handful of fields this package reads out of
// charts/audit/testdata/values/e2e.yaml — everything else in that file is
// the chart's own business.
type chartValues struct {
	Bucket     string `json:"bucket"`
	Region     string `json:"region"`
	Endpoint   string `json:"endpoint"`
	PathStyle  bool   `json:"pathStyle"`
	LockMode   string `json:"lockMode"`
	ExistingS3 string `json:"existingSecret"`
	Database   struct {
		ExistingSecret string `json:"existingSecret"`
	} `json:"database"`
	Stream struct {
		URL      string `json:"url"`
		Name     string `json:"name"`
		Consumer string `json:"consumer"`
	} `json:"stream"`
	Query struct {
		Database struct {
			ExistingSecret string `json:"existingSecret"`
			Role           string `json:"role"`
		} `json:"database"`
	} `json:"query"`
	Jobs struct {
		Digest struct {
			SigningKey struct {
				ExistingSecret string `json:"existingSecret"`
				KeyID          string `json:"keyID"`
			} `json:"signingKey"`
		} `json:"digest"`
		Verify struct {
			PublicKey struct {
				ExistingSecret string `json:"existingSecret"`
			} `json:"publicKey"`
		} `json:"verify"`
	} `json:"jobs"`
}

// Names is everything the fixture creates, and everything the chart install
// must be given to find it.
type Names struct {
	Options

	// Database.
	DatabaseHost string // the box's one Postgres server
	DatabaseName string // fixture's own choice: the database the writer's role owns
	WriterRole   string // fixture's own choice: owns the schema, migrates, and writes the index
	QueryRole    string // read the values file's own query.database.role: the migrate job grants THIS role, by name
	WriterSecret string // database.existingSecret
	QuerySecret  string // query.database.existingSecret

	// The wide stream.
	StreamURL      string
	StreamName     string
	StreamSubject  string // fixture's own choice: the one subject the stream carries
	StreamConsumer string // stream.consumer — the writer binds its OWN durable consumer under this name

	// The archive.
	Bucket        string
	Region        string
	Endpoint      string
	PathStyle     bool
	LockMode      string
	S3CredsSecret string // existingSecret: AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY

	// The digest chain's signing key.
	DigestKeySecret    string
	DigestKeyID        string
	VerifyPublicSecret string
}

// valuesFilePath resolves charts/audit/testdata/values/e2e.yaml relative to
// THIS source file, not the caller's working directory — `go run` from the
// repository root and `go test` from this package's own directory must both
// find it.
func valuesFilePath() string {
	_, this, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(this), "..", "..", "charts", "audit", "testdata", "values", "e2e.yaml")
}

// Resolve reads charts/audit/testdata/values/e2e.yaml and returns the names
// this package creates and the chart install must be given.
func Resolve(o Options) (Names, error) {
	o = o.withDefaults()

	raw, err := os.ReadFile(valuesFilePath())
	if err != nil {
		return Names{}, fmt.Errorf("read %s: %w", valuesFilePath(), err)
	}
	var v chartValues
	if err := yaml.Unmarshal(raw, &v); err != nil {
		return Names{}, fmt.Errorf("parse %s: %w", valuesFilePath(), err)
	}

	for field, got := range map[string]string{
		"bucket":                                v.Bucket,
		"existingSecret":                        v.ExistingS3,
		"database.existingSecret":               v.Database.ExistingSecret,
		"stream.url":                            v.Stream.URL,
		"stream.name":                           v.Stream.Name,
		"stream.consumer":                       v.Stream.Consumer,
		"query.database.existingSecret":         v.Query.Database.ExistingSecret,
		"query.database.role":                   v.Query.Database.Role,
		"jobs.digest.signingKey.existingSecret": v.Jobs.Digest.SigningKey.ExistingSecret,
		"jobs.digest.signingKey.keyID":          v.Jobs.Digest.SigningKey.KeyID,
		"jobs.verify.publicKey.existingSecret":  v.Jobs.Verify.PublicKey.ExistingSecret,
	} {
		if got == "" {
			return Names{}, fmt.Errorf("%s: %s is empty — this package has nothing to name", valuesFilePath(), field)
		}
	}

	return Names{
		Options: o,

		DatabaseHost: boxPostgresAddress,
		DatabaseName: "audit_e2e",
		WriterRole:   "audit_e2e_writer",
		QueryRole:    v.Query.Database.Role,
		WriterSecret: v.Database.ExistingSecret,
		QuerySecret:  v.Query.Database.ExistingSecret,

		StreamURL:      v.Stream.URL,
		StreamName:     v.Stream.Name,
		StreamSubject:  "e2e.audit-records",
		StreamConsumer: v.Stream.Consumer,

		Bucket:        v.Bucket,
		Region:        v.Region,
		Endpoint:      v.Endpoint,
		PathStyle:     v.PathStyle,
		LockMode:      v.LockMode,
		S3CredsSecret: v.ExistingS3,

		DigestKeySecret:    v.Jobs.Digest.SigningKey.ExistingSecret,
		DigestKeyID:        v.Jobs.Digest.SigningKey.KeyID,
		VerifyPublicSecret: v.Jobs.Verify.PublicKey.ExistingSecret,
	}, nil
}
