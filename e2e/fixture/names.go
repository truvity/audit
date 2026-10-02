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
// The database, its host and the writer's role are one fact, the URL in the
// writer's config; the query role is the migration's `reader`. What each
// Secret holds is this package's own choice beyond its name and its key: the
// database password under `password`, and the connection string too, under
// `url`, for the suite that connects from outside.
package fixture

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"sigs.k8s.io/yaml"
)

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

// secretEnv is one entry of a component's `secretEnv`: a Secret's key, put in
// the variable its config names.
type secretEnv struct {
	Name       string `json:"name"`
	SecretName string `json:"secretName"`
	Key        string `json:"key"`
}

type secretMount struct {
	SecretName string `json:"secretName"`
	MountPath  string `json:"mountPath"`
}

// chartValues is the handful of fields this package reads out of
// charts/audit/testdata/values/e2e.yaml — everything else in that file is
// the chart's own business. They are the binaries' own configuration keys,
// read from where the chart's values carry them.
type chartValues struct {
	Writer struct {
		Config struct {
			Archive struct {
				Bucket struct {
					Name      string `json:"name"`
					Region    string `json:"region"`
					Endpoint  string `json:"endpoint"`
					PathStyle bool   `json:"pathStyle"`
				} `json:"bucket"`
				LockMode string `json:"lockMode"`
			} `json:"archive"`
			Stream struct {
				NATS struct {
					URL string `json:"url"`
				} `json:"nats"`
				Name     string `json:"name"`
				Consumer string `json:"consumer"`
			} `json:"stream"`
		} `json:"config"`
		SecretEnv []secretEnv `json:"secretEnv"`
	} `json:"writer"`
	Migrate struct {
		Config struct {
			Database struct {
				URL         string `json:"url"`
				PasswordEnv string `json:"passwordEnv"`
			} `json:"database"`
			Reader string `json:"reader"`
		} `json:"config"`
		SecretEnv []secretEnv `json:"secretEnv"`
	} `json:"migrate"`
	Jobs struct {
		Digest struct {
			Config struct {
				Signer struct {
					KeyFile struct {
						ID string `json:"id"`
					} `json:"keyFile"`
				} `json:"signer"`
			} `json:"config"`
			SecretMounts []secretMount `json:"secretMounts"`
		} `json:"digest"`
		Verify struct {
			SecretMounts []secretMount `json:"secretMounts"`
		} `json:"verify"`
	} `json:"jobs"`
}

// secretOf finds the Secret a variable is taken from.
func secretOf(env []secretEnv, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.SecretName
		}
	}
	return ""
}

func firstMount(m []secretMount) string {
	if len(m) == 0 {
		return ""
	}
	return m[0].SecretName
}

// queryDatabaseSecret is the query role's credential, a Secret this package
// names itself: the e2e suite reads it, and the chart's values never do, since
// the query service is off in this tier.
const queryDatabaseSecret = "audit-e2e-query-database"

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

	// The database is named by the URL the config carries: its host, its
	// name and the role it connects as are one fact, written once.
	db, err := url.Parse(v.Migrate.Config.Database.URL)
	if err != nil {
		return Names{}, fmt.Errorf("%s: migrate.config.database.url: %w", valuesFilePath(), err)
	}
	writerSecret := secretOf(v.Migrate.SecretEnv, v.Migrate.Config.Database.PasswordEnv)
	s3Secret := secretOf(v.Writer.SecretEnv, "AUDIT_S3_ACCESS_KEY_ID")

	for field, got := range map[string]string{
		"writer.config.archive.bucket.name":                  v.Writer.Config.Archive.Bucket.Name,
		"migrate.config.database.url (host)":                 db.Host,
		"migrate.config.database.url (user)":                 db.User.Username(),
		"migrate.config.database.url (database)":             strings.TrimPrefix(db.Path, "/"),
		"migrate.secretEnv (the database password's Secret)": writerSecret,
		"writer.secretEnv (AUDIT_S3_ACCESS_KEY_ID's Secret)": s3Secret,
		"writer.config.stream.nats.url":                      v.Writer.Config.Stream.NATS.URL,
		"writer.config.stream.name":                          v.Writer.Config.Stream.Name,
		"writer.config.stream.consumer":                      v.Writer.Config.Stream.Consumer,
		"migrate.config.reader":                              v.Migrate.Config.Reader,
		"jobs.digest.secretMounts[0].secretName":             firstMount(v.Jobs.Digest.SecretMounts),
		"jobs.digest.config.signer.keyFile.id":               v.Jobs.Digest.Config.Signer.KeyFile.ID,
		"jobs.verify.secretMounts[0].secretName":             firstMount(v.Jobs.Verify.SecretMounts),
	} {
		if got == "" {
			return Names{}, fmt.Errorf("%s: %s is empty — this package has nothing to name", valuesFilePath(), field)
		}
	}

	return Names{
		Options: o,

		DatabaseHost: strings.TrimSuffix(db.Host, ":"+db.Port()),
		DatabaseName: strings.TrimPrefix(db.Path, "/"),
		WriterRole:   db.User.Username(),
		QueryRole:    v.Migrate.Config.Reader,
		WriterSecret: writerSecret,
		QuerySecret:  queryDatabaseSecret,

		StreamURL:      v.Writer.Config.Stream.NATS.URL,
		StreamName:     v.Writer.Config.Stream.Name,
		StreamSubject:  "e2e.audit-records",
		StreamConsumer: v.Writer.Config.Stream.Consumer,

		Bucket:        v.Writer.Config.Archive.Bucket.Name,
		Region:        v.Writer.Config.Archive.Bucket.Region,
		Endpoint:      v.Writer.Config.Archive.Bucket.Endpoint,
		PathStyle:     v.Writer.Config.Archive.Bucket.PathStyle,
		LockMode:      v.Writer.Config.Archive.LockMode,
		S3CredsSecret: s3Secret,

		DigestKeySecret:    firstMount(v.Jobs.Digest.SecretMounts),
		DigestKeyID:        v.Jobs.Digest.Config.Signer.KeyFile.ID,
		VerifyPublicSecret: firstMount(v.Jobs.Verify.SecretMounts),
	}, nil
}
