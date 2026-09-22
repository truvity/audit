package cli

import (
	"flag"
	"strings"
	"testing"

	"github.com/truvity/audit/store/s3store"
)

func lookupFrom(env map[string]string) func(name, fallback string) string {
	return func(name, fallback string) string {
		if v, ok := env[name]; ok {
			return v
		}
		return fallback
	}
}

// The environment the chart sets is read into the flags, and the store
// options carry it through: a pod on an S3-compatible store needs no
// arguments the chart has to know the names of.
func TestArchiveFlagsReadTheEnvironment(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	a := NewArchiveFlags(fs, lookupFrom(map[string]string{
		"AUDIT_BUCKET": "archive", "AUDIT_PREFIX": "audit/app", "AWS_REGION": "auto",
		"AUDIT_S3_ENDPOINT": "https://s3.example.test", "AUDIT_S3_PATH_STYLE": "true",
		"AUDIT_LOCK_MODE": "none",
	}), Writes)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	o, err := a.Options()
	if err != nil {
		t.Fatal(err)
	}
	want := s3store.Options{
		Bucket: "archive", Prefix: "audit/app", Lock: s3store.None,
		Endpoint: "https://s3.example.test", PathStyle: true,
	}
	if o != want {
		t.Fatalf("options = %+v, want %+v", o, want)
	}
	if *a.Region != "auto" {
		t.Fatalf("region = %q", *a.Region)
	}
}

// With nothing set the options are what an AWS deployment always had: the
// bucket, compliance, no endpoint.
func TestArchiveFlagsDefaultToAWSAndCompliance(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	a := NewArchiveFlags(fs, nil, Writes)
	if err := fs.Parse([]string{"--bucket", "b"}); err != nil {
		t.Fatal(err)
	}
	o, err := a.Options()
	if err != nil {
		t.Fatal(err)
	}
	if o != (s3store.Options{Bucket: "b", Lock: s3store.Compliance}) {
		t.Fatalf("options = %+v", o)
	}
	if _, err := NewArchiveFlags(flag.NewFlagSet("t", flag.ContinueOnError), nil, Writes).Options(); err == nil ||
		!strings.Contains(err.Error(), "--bucket") {
		t.Fatalf("no bucket should be refused by name, got %v", err)
	}
}

// A command that only reads registers no --lock-mode; a command that writes
// takes the deprecated --governance as --lock-mode governance, unless
// --lock-mode was given explicitly and disagrees.
func TestLockModeFlagAndItsAlias(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	NewArchiveFlags(fs, nil, Reads)
	if fs.Lookup("lock-mode") != nil {
		t.Fatal("a reader was given a lock mode to set")
	}

	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	a := NewArchiveFlags(fs, nil, Writes)
	if err := fs.Parse([]string{"--bucket", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetLock(s3store.Governance); err != nil {
		t.Fatal(err)
	}
	if mode, _ := a.Lock(); mode != s3store.Governance {
		t.Fatalf("lock = %q, want the alias applied", mode)
	}

	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	a = NewArchiveFlags(fs, nil, Writes)
	if err := fs.Parse([]string{"--bucket", "b", "--lock-mode", "none"}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetLock(s3store.Governance); err == nil || !strings.Contains(err.Error(), "say one thing") {
		t.Fatalf("two answers to one question should be refused, got %v", err)
	}
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	a = NewArchiveFlags(fs, nil, Writes)
	if err := fs.Parse([]string{"--bucket", "b", "--lock-mode", "governance"}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetLock(s3store.Governance); err != nil {
		t.Fatalf("the alias agreeing with the flag is not a conflict: %v", err)
	}

	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	a = NewArchiveFlags(fs, nil, Writes)
	if err := fs.Parse([]string{"--bucket", "b", "--lock-mode", "unlocked"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Options(); err == nil {
		t.Fatal("a lock mode that is not one was accepted")
	}
}

// Exports inherit the archive's store unless they name their own, and never
// a lock.
func TestExportFlagsInheritTheArchivesStore(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	a := NewArchiveFlags(fs, lookupFrom(map[string]string{
		"AUDIT_S3_ENDPOINT": "https://s3.example.test", "AUDIT_S3_PATH_STYLE": "1",
	}), Reads)
	e := NewExportFlags(fs, lookupFrom(map[string]string{"AUDIT_EXPORTS": "exports"}))
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if *e.Bucket != "exports" || *e.Endpoint != "" || *e.PathStyle {
		t.Fatalf("exports = %q at %q path-style %v", *e.Bucket, *e.Endpoint, *e.PathStyle)
	}
	o := e.options(a)
	if o.Endpoint != *a.Endpoint || !o.PathStyle || o.Lock != s3store.None {
		t.Fatalf("exports options = %+v, want the archive's store and no lock", o)
	}

	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	a = NewArchiveFlags(fs, lookupFrom(map[string]string{"AUDIT_S3_ENDPOINT": "https://s3.example.test"}), Reads)
	e = NewExportFlags(fs, nil)
	if err := fs.Parse([]string{"--exports", "x", "--exports-endpoint", "https://files.example.test"}); err != nil {
		t.Fatal(err)
	}
	if o := e.options(a); o.Endpoint != "https://files.example.test" || o.PathStyle {
		t.Fatalf("exports options = %+v, want their own store", o)
	}
}
