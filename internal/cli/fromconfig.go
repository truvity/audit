package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/truvity/audit/internal/config"
	"github.com/truvity/audit/keys"
	"github.com/truvity/audit/sdk/auth"
	"github.com/truvity/audit/sdk/sink"
	"github.com/truvity/audit/sink/sqssink"
	"github.com/truvity/audit/store/s3store"
)

// What follows builds the things a binary opens from its configuration file.
// The flags that built the same things in the interactive commands are above;
// these read no flag and no variable the file did not name.

// awsConfig is the SDK's configuration for one bucket: the ambient identity a
// workload has, unless the file names variables holding static credentials,
// and a CA bundle when the store's certificate is not signed by a public root.
func awsConfig(ctx context.Context, b config.Bucket) (aws.Config, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if b.Region != "" {
		opts = append(opts, awsconfig.WithRegion(b.Region))
	}
	if b.CA != "" {
		bundle, err := os.Open(b.CA)
		if err != nil {
			return aws.Config{}, fmt.Errorf("bucket.ca: %w", err)
		}
		defer bundle.Close() //nolint:errcheck // read once
		opts = append(opts, awsconfig.WithCustomCABundle(bundle))
	}
	if c := b.CredentialsEnv; c != nil {
		id, err := config.Secret(c.AccessKeyID)
		if err != nil {
			return aws.Config{}, fmt.Errorf("credentialsEnv.accessKeyID: %w", err)
		}
		secret, err := config.Secret(c.SecretAccessKey)
		if err != nil {
			return aws.Config{}, fmt.Errorf("credentialsEnv.secretAccessKey: %w", err)
		}
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(id, secret, "")))
	}
	return awsconfig.LoadDefaultConfig(ctx, opts...)
}

// OpenArchiveFrom opens the archive the configuration names.
func OpenArchiveFrom(ctx context.Context, a config.Archive) (*s3store.Store, error) {
	lock, err := s3store.ParseLockMode(a.LockMode)
	if err != nil {
		return nil, err
	}
	cfg, err := awsConfig(ctx, a.Bucket)
	if err != nil {
		return nil, err
	}
	return s3store.FromConfig(cfg, s3store.Options{
		Bucket: a.Bucket.Name, Prefix: a.Prefix, Lock: lock, KMSKeyID: a.KMSKey,
		Endpoint: a.Bucket.Endpoint, PathStyle: a.Bucket.PathStyle,
	})
}

// OpenExportsFrom opens the exports bucket, which is a store of its own and has
// no lock: an export is a copy made to be taken away and then cleared.
func OpenExportsFrom(ctx context.Context, b config.Bucket) (*s3store.Store, error) {
	cfg, err := awsConfig(ctx, b)
	if err != nil {
		return nil, err
	}
	return s3store.FromConfig(cfg, s3store.Options{
		Bucket: b.Name, Lock: s3store.None, Endpoint: b.Endpoint, PathStyle: b.PathStyle,
	})
}

// Credentials are how to sign in to OpenBAO: a JWT login, a token file, or a
// token read from the variable the file names.
func openBAOCredentials(o config.OpenBAO) (login *keys.JWTLogin, token, tokenFile string, err error) {
	switch {
	case o.Login != nil:
		return &keys.JWTLogin{Mount: o.Login.Mount, Role: o.Login.Role, TokenFile: o.Login.JWTFile}, "", "", nil
	case o.TokenFile != "":
		return nil, "", o.TokenFile, nil
	default:
		token, err = config.Secret(o.TokenEnv)
		if err != nil {
			return nil, "", "", fmt.Errorf("openbao.tokenEnv: %w", err)
		}
		return nil, token, "", nil
	}
}

func mountOrTransit(m string) string {
	if m == "" {
		return "transit"
	}
	return m
}

// OpenKeysFrom opens the key provider the configuration names. It is nil where
// a deployment runs without one, which is the default.
func OpenKeysFrom(ctx context.Context, k *config.Keys) (keys.Provider, error) {
	switch {
	case !k.Enabled():
		return nil, nil
	case k.IsLocal():
		root, err := os.ReadFile(k.Local.RootFile)
		if err != nil {
			return nil, fmt.Errorf("keys.local.rootFile: %w", err)
		}
		return keys.NewLocal(root, k.Local.Dir)
	default:
		t := k.Transit
		login, token, tokenFile, err := openBAOCredentials(t.OpenBAO)
		if err != nil {
			return nil, err
		}
		prefix := t.Prefix
		if prefix == "" {
			prefix = "audit"
		}
		return keys.NewTransit(ctx, &keys.Transit{
			Address: t.OpenBAO.Address, Mount: mountOrTransit(t.OpenBAO.Mount), Namespace: t.OpenBAO.Namespace,
			CAFile: t.OpenBAO.CAFile, Prefix: prefix, Login: login, Token: token, TokenFile: tokenFile,
		})
	}
}

// OpenSignerFrom opens the key the configuration says seals are signed with.
// The private half of a managed key (kms, transit) never reaches this process;
// the file is the exception, and the one to leave to development.
func OpenSignerFrom(ctx context.Context, s config.Signer) (keys.Signer, error) {
	switch {
	case s.KMS != nil:
		cfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("signer.kms: %w", err)
		}
		if s.KMS.Region != "" {
			cfg.Region = s.KMS.Region
		}
		return &keys.KMSSigner{Client: kms.NewFromConfig(cfg), Key: s.KMS.Key}, nil
	case s.Transit != nil:
		login, token, tokenFile, err := openBAOCredentials(s.Transit.OpenBAO)
		if err != nil {
			return nil, err
		}
		o := s.Transit.OpenBAO
		return keys.NewTransitSigner(ctx, &keys.TransitSigner{
			Address: o.Address, Mount: mountOrTransit(o.Mount), Namespace: o.Namespace, CAFile: o.CAFile,
			Key: s.Transit.Key, Login: login, Token: token, TokenFile: tokenFile,
		})
	case s.File != nil:
		return keys.LoadLocalSignerFile("", s.File.Path)
	}
	return nil, fmt.Errorf("signer: name one of kms, transit and file")
}

// SinkFrom is a client to the writer the configuration names, presenting the
// token in the file it names. Every job that records through the writer builds
// its client here or in WriterClient, so that none of them is the one that
// forgot.
//
// `expect` says what the writer is configured to give, and `require` is the
// floor the process holds it to: with a require, the client is wrapped in
// sink.Guard, which refuses at start-up when the expectation is below it and
// fails any acknowledgement weaker than it afterwards. Without one the client
// is returned as it is.
func SinkFrom(s config.Sink, require string) (sink.Sink, error) {
	if s.SQS != nil {
		return sqsSinkFrom(s, require)
	}
	var c *sink.Client
	if s.TokenFile != "" {
		c = sink.NewClient(auth.TokenFile(s.TokenFile), s.URL)
	} else {
		c = sink.NewClient(nil, s.URL)
	}
	if s.Expect != "" {
		d, err := sink.ParseDurability(s.Expect)
		if err != nil {
			return nil, fmt.Errorf("sink.expect: %w", err)
		}
		c = c.Expecting(d)
	}
	if require == "" {
		return c, nil
	}
	least, err := sink.ParseDurability(require)
	if err != nil {
		return nil, fmt.Errorf("require: %w", err)
	}
	guarded, err := sink.Guard(c, least)
	if err != nil {
		return nil, fmt.Errorf("require: %s: the writer at sink.url is configured to give %s: %w",
			require, orUnspecified(s.Expect), err)
	}
	return guarded, nil
}

func orUnspecified(s string) string {
	if s == "" {
		return "nothing it says (set sink.expect)"
	}
	return s
}

// newSQS opens the SQS client of a sink that names a queue: the SDK's ambient
// identity, which on Kubernetes is the pod's (EKS Pod Identity or IRSA). A test
// replaces it with a fake queue.
var newSQS = func(ctx context.Context, region string) (sqssink.API, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading the AWS configuration for SQS: %w", err)
	}
	return sqs.NewFromConfig(cfg), nil
}

// sqsSinkFrom is a sink that sends to the ingest queue of a writer running
// elsewhere. The acknowledgement is `queued`, and the guard holds it to
// `require` like any other.
func sqsSinkFrom(s config.Sink, require string) (sink.Sink, error) {
	api, err := newSQS(context.Background(), s.SQS.Region)
	if err != nil {
		return nil, err
	}
	p, err := sqssink.NewPublisher(api, sqssink.Options{QueueURL: s.SQS.QueueURL})
	if err != nil {
		return nil, err
	}
	if require == "" {
		return p, nil
	}
	least, err := sink.ParseDurability(require)
	if err != nil {
		return nil, fmt.Errorf("require: %w", err)
	}
	guarded, err := sink.Guard(p, least)
	if err != nil {
		return nil, fmt.Errorf("require: %s: the queue at sink.sqs gives queued: %w", require, err)
	}
	return guarded, nil
}
