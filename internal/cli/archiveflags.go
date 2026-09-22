package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/truvity/audit/store/s3store"
)

// ArchiveFlags are the flags every command that opens the archive shares:
// where it is, which store it is on, and -- for a command that writes to it
// -- which lock it writes with.
//
// They are one set so that the writer, the jobs and the query service take
// the same names and read the same environment (AUDIT_BUCKET, AUDIT_PREFIX,
// AWS_REGION, AUDIT_S3_ENDPOINT, AUDIT_S3_PATH_STYLE, AUDIT_LOCK_MODE), which
// is what lets the chart set them once per pod. Static credentials need no
// flag: the SDK reads AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY itself, and
// AWS_ENDPOINT_URL_S3 as well, so an endpoint may also arrive that way.
type ArchiveFlags struct {
	Bucket, Prefix, Region, Endpoint *string
	PathStyle                        *bool
	// lock is registered only for a command that writes; see Lock.
	lock *string
	fs   *flag.FlagSet
}

// Reads and Writes say whether a command puts objects. A command that only
// reads takes no --lock-mode: the lock is a property of what is written.
const (
	Reads  = false
	Writes = true
)

// NewArchiveFlags registers the archive flags on a flag set. Lookup gives each
// flag's default from the environment; nil means no environment.
func NewArchiveFlags(fs *flag.FlagSet, lookup func(name, fallback string) string, writes bool) *ArchiveFlags {
	if lookup == nil {
		lookup = func(_, fallback string) string { return fallback }
	}
	a := &ArchiveFlags{
		Bucket: fs.String("bucket", lookup("AUDIT_BUCKET", ""), "the bucket the archive is in"),
		Prefix: fs.String("prefix", lookup("AUDIT_PREFIX", ""), "the prefix within the bucket"),
		Region: fs.String("region", lookup("AWS_REGION", ""), "the region, when it is not in the environment"),
		Endpoint: fs.String("endpoint", lookup("AUDIT_S3_ENDPOINT", ""),
			"the store's URL, for an S3-compatible store that is not AWS; empty is AWS, or AWS_ENDPOINT_URL_S3"),
		PathStyle: fs.Bool("path-style", isTrue(lookup("AUDIT_S3_PATH_STYLE", "")),
			"address the bucket as endpoint/bucket/key, for a store whose certificate does not cover a bucket subdomain"),
		fs: fs,
	}
	if writes {
		a.lock = fs.String("lock-mode", lookup("AUDIT_LOCK_MODE", string(s3store.Compliance)),
			"the Object Lock mode every object is written in: compliance (the default), governance, or "+
				"none for a store without Object Lock; a profile that demands a stricter mode refuses to start")
	}
	return a
}

// Lock is the mode the command writes in. A command registered with Reads
// has none to give and answers compliance, which nothing it does depends on.
func (a *ArchiveFlags) Lock() (s3store.LockMode, error) {
	if a.lock == nil {
		return s3store.Compliance, nil
	}
	return s3store.ParseLockMode(*a.lock)
}

// SetLock is what a deprecated alias of --lock-mode does: it sets the mode
// unless --lock-mode was given explicitly and says otherwise, which is two
// answers to one question and refused.
func (a *ArchiveFlags) SetLock(mode s3store.LockMode) error {
	explicit := false
	a.fs.Visit(func(f *flag.Flag) {
		if f.Name == "lock-mode" {
			explicit = true
		}
	})
	if explicit && *a.lock != string(mode) {
		return fmt.Errorf("--lock-mode %s and a flag that means --lock-mode %s were both given; say one thing", *a.lock, mode)
	}
	*a.lock = string(mode)
	return nil
}

// Options are the store options the flags describe.
func (a *ArchiveFlags) Options() (s3store.Options, error) {
	if *a.Bucket == "" {
		return s3store.Options{}, errors.New("name the archive's bucket with --bucket")
	}
	lock, err := a.Lock()
	if err != nil {
		return s3store.Options{}, err
	}
	return s3store.Options{
		Bucket: *a.Bucket, Prefix: *a.Prefix, Lock: lock,
		Endpoint: *a.Endpoint, PathStyle: *a.PathStyle,
	}, nil
}

// Open opens the archive the flags name, on the ambient AWS configuration.
func (a *ArchiveFlags) Open(ctx context.Context) (*s3store.Store, error) {
	o, err := a.Options()
	if err != nil {
		return nil, err
	}
	return OpenStore(ctx, *a.Region, o)
}

// OpenStore opens a store on the ambient AWS configuration, which in a
// cluster is the workload's own identity, or the static credentials the
// environment carries.
func OpenStore(ctx context.Context, region string, o s3store.Options) (*s3store.Store, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	if region != "" {
		cfg.Region = region
	}
	return s3store.FromConfig(cfg, o)
}

// ExportFlags are the flags for the bucket exports are written to: its own
// bucket, on a store of its own if need be, with no lock.
type ExportFlags struct {
	Bucket, Endpoint *string
	PathStyle        *bool
	lookup           func(name, fallback string) string
}

// NewExportFlags registers the export flags. The bucket is --exports, as it
// always was; the store it is on takes --exports-endpoint and
// --exports-path-style (AUDIT_EXPORTS_ENDPOINT, AUDIT_EXPORTS_PATH_STYLE),
// and credentials of its own, when the archive's do not reach it, from
// AUDIT_EXPORTS_ACCESS_KEY_ID, AUDIT_EXPORTS_SECRET_ACCESS_KEY and, if there
// is one, AUDIT_EXPORTS_SESSION_TOKEN.
func NewExportFlags(fs *flag.FlagSet, lookup func(name, fallback string) string) *ExportFlags {
	if lookup == nil {
		lookup = func(_, fallback string) string { return fallback }
	}
	return &ExportFlags{
		Bucket: fs.String("exports", lookup("AUDIT_EXPORTS", ""),
			"the bucket exports are written to; without it the export operation is refused"),
		Endpoint: fs.String("exports-endpoint", lookup("AUDIT_EXPORTS_ENDPOINT", ""),
			"the store the exports bucket is on, when it is not the archive's; empty is the archive's"),
		PathStyle: fs.Bool("exports-path-style", isTrue(lookup("AUDIT_EXPORTS_PATH_STYLE", "")),
			"address the exports bucket as endpoint/bucket/key"),
		lookup: lookup,
	}
}

// Open opens the exports bucket, without a lock.
//
// It is a different store from the archive on purpose: an export is a copy of
// records made to be taken away and then cleared, and a lock would keep it. A
// bucket without Object Lock refuses a put that names a lock mode, so this is
// also the only way to write to one. The archive's flags give the endpoint
// and path style the exports inherit when they name none of their own.
func (e *ExportFlags) Open(ctx context.Context, region string, archive *ArchiveFlags) (*s3store.Store, error) {
	if *e.Bucket == "" {
		return nil, errors.New("name the exports bucket with --exports")
	}
	o := e.options(archive)
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	if region != "" {
		cfg.Region = region
	}
	// A second set of static credentials, for an exports bucket on a store
	// the archive's credentials do not reach. The SDK's own variables name
	// one identity per process; these name the other.
	if id, secret := e.lookup("AUDIT_EXPORTS_ACCESS_KEY_ID", ""), e.lookup("AUDIT_EXPORTS_SECRET_ACCESS_KEY", ""); id != "" && secret != "" {
		cfg.Credentials = aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(
			id, secret, e.lookup("AUDIT_EXPORTS_SESSION_TOKEN", "")))
	}
	return s3store.FromConfig(cfg, o)
}

// options are the exports store's: the archive's endpoint and path style
// unless the exports name their own, and never a lock.
func (e *ExportFlags) options(archive *ArchiveFlags) s3store.Options {
	o := s3store.Options{Bucket: *e.Bucket, Lock: s3store.None, Endpoint: *e.Endpoint, PathStyle: *e.PathStyle}
	if o.Endpoint == "" && archive != nil {
		o.Endpoint, o.PathStyle = *archive.Endpoint, *archive.PathStyle
	}
	return o
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
