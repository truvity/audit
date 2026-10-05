package auditpulumi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	yaml "go.yaml.in/yaml/v3"
)

// The guards run in the program, before the function is created or updated. A
// function that fails its init is not a failed deploy: the event source mapping
// keeps invoking it, every invocation fails, and the ingest queue drains into
// the dead-letter queue, a message at a time, until somebody looks. So what can
// be known before the function is touched is checked before it is touched, and
// a failure is a failed `pulumi preview`.

// GuardArgs are the switches of the pre-deploy guards. Each is a way to say
// that a refusal is understood, and none is the default.
type GuardArgs struct {
	// AllowVersionSkew accepts a function binary whose release is not the
	// library's: a build from a checkout ("dev"), or a library and a binary that
	// are different on purpose. The library renders configuration for ITS
	// release's schema, and a binary of another one may refuse it at start-up.
	AllowVersionSkew bool
	// SkipCatalogueCheck does not compare the writer's catalogues with the
	// archive's copies. For a deployer whose credentials cannot read the bucket
	// at deploy time, and for the first deploy of an archive that exists already
	// and is not readable from here. The writer still refuses to start on a
	// changed catalogue under an unchanged version, which is what the check
	// exists to say before it does.
	SkipCatalogueCheck bool
}

const libraryModule = "github.com/truvity/audit/deploy/pulumi"

// libraryVersion is the release of this library as its caller was built with it,
// without the leading v, and "" when that is not known: a build of the module
// itself, or a replace directive, says nothing about a release. A test sets it.
var libraryVersion = func() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, d := range bi.Deps {
		if d.Path != libraryModule {
			continue
		}
		if d.Replace != nil || d.Version == "" || d.Version == "(devel)" {
			return ""
		}
		return strings.TrimPrefix(d.Version, "v")
	}
	return ""
}

// checkVersions holds each function's binary to the library's release. With the
// library's release unknown there is nothing to compare, and the guard says
// nothing; with the binary's unknown or different, it refuses.
func checkVersions(g GuardArgs, pkgs map[string]*releasePackage) error {
	lib := libraryVersion()
	if lib == "" || g.AllowVersionSkew {
		return nil
	}
	fields := make([]string, 0, len(pkgs))
	for f := range pkgs {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	for _, f := range fields {
		bin := pkgs[f].Version
		switch bin {
		case lib:
		case "":
			return fmt.Errorf("auditpulumi: %s.Package is not named as the release names its zips (<command>_<version>_linux_arm64.zip), "+
				"so which release it is cannot be told, and this library is %s: use the release's file name, "+
				"or set Guards.AllowVersionSkew if the difference is meant", f, lib)
		default:
			return fmt.Errorf("auditpulumi: %s.Package is release %s and this library is %s: the library renders the configuration "+
				"for its own release, and a function that refuses it at start-up drains the ingest queue into the dead-letter queue. "+
				"Use the release's own zip, or set Guards.AllowVersionSkew if the difference is meant", f, bin, lib)
		}
	}
	return nil
}

// catalogueIdentity reads the two keys that say which catalogue a document is.
func catalogueIdentity(file, doc string) (source, version string, err error) {
	var id struct {
		Source  string `yaml:"source"`
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal([]byte(doc), &id); err != nil {
		return "", "", fmt.Errorf("auditpulumi: Writer.Catalogues %s is not YAML: %w", file, err)
	}
	if id.Source == "" || id.Version == "" {
		return "", "", fmt.Errorf("auditpulumi: Writer.Catalogues %s names no source or no version: a catalogue is registered by both", file)
	}
	return id.Source, id.Version, nil
}

// checkCatalogues compares each catalogue the writer will register with the
// archive's own copy at catalogue/<source>/<version>. The archive holds a
// catalogue exactly as it was registered, written once and never replaced, and
// the writer refuses to start when the bytes it is given are not the bytes
// already there: a changed document under an unchanged version. This asks the
// archive first, so that the refusal is a failed preview and not a function
// that cannot initialise.
//
// An object that is not there, or a bucket that is not there yet (a first
// deploy), is nothing to compare against, and passes. Anything else that stops
// the question being answered is a refusal: a guard that passes when it could
// not look is not one.
func checkCatalogues(ctx *pulumi.Context, a *Args, opts ...pulumi.InvokeOption) error {
	if a.Ingest.Disabled || a.Guards.SkipCatalogueCheck || len(a.Writer.Catalogues) == 0 {
		return nil
	}
	files := make([]string, 0, len(a.Writer.Catalogues))
	for f := range a.Writer.Catalogues {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		doc := a.Writer.Catalogues[f]
		source, version, err := catalogueIdentity(f, doc)
		if err != nil {
			return err
		}
		key := "catalogue/" + source + "/" + version
		got, err := s3.GetObject(ctx, &s3.GetObjectArgs{
			Bucket: a.Archive.BucketName, Key: key, DownloadBody: pulumi.StringRef("false"),
		}, opts...)
		if err != nil {
			if absent(err) {
				continue
			}
			if denied(err) {
				return fmt.Errorf("auditpulumi: reading %s from the archive bucket was denied: %w. S3 answers a missing key with 403 when the "+
					"deploying identity lacks s3:ListBucket, so it needs s3:GetObject on catalogue/* AND s3:ListBucket on the bucket "+
					"(condition s3:prefix = catalogue/); Guards.SkipCatalogueCheck skips this comparison", key, err)
			}
			return fmt.Errorf("auditpulumi: could not read %s from the archive bucket to compare it with Writer.Catalogues %s: %w "+
				"(the deploying identity needs s3:GetObject on catalogue/*; Guards.SkipCatalogueCheck skips this comparison)", key, f, err)
		}
		sum := sha256.Sum256([]byte(doc))
		want := hex.EncodeToString(sum[:])
		held := metadata(got.Metadata, "sha256")
		if held == "" {
			return fmt.Errorf("auditpulumi: %s is in the archive and carries no sha256 metadata, so it cannot be compared with Writer.Catalogues %s "+
				"(Guards.SkipCatalogueCheck skips this comparison)", key, f)
		}
		if !strings.EqualFold(held, want) {
			return fmt.Errorf("auditpulumi: Writer.Catalogues %s is %s version %s, which the archive already holds with other content "+
				"(sha256 %s there, %s here). A changed catalogue is a new version: change `version:` in the document. "+
				"The writer would refuse to start on this, and the ingest queue would drain into the dead-letter queue", f, source, version, held, want)
		}
	}
	return nil
}

// absent says whether an error from reading an object is that the object, or its
// bucket, is not there.
func absent(err error) bool {
	m := err.Error()
	// The provider's data source wraps a 404 as a not-found error whose text is
	// "reading S3 Bucket (b) Object (k): couldn't find resource", with none of the
	// SDK's own words in it.
	for _, s := range []string{"couldn't find resource", "NoSuchKey", "NoSuchBucket", "NotFound", "StatusCode: 404"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

func denied(err error) bool {
	m := err.Error()
	return strings.Contains(m, "AccessDenied") || strings.Contains(m, "StatusCode: 403") || strings.Contains(m, "Forbidden")
}

func metadata(m map[string]string, key string) string {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}
