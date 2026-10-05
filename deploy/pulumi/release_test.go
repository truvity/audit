package auditpulumi_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	auditpulumi "github.com/truvity/audit/deploy/pulumi"
)

// cloudResources counts what was declared beside the component itself, which is a
// name for the group and holds nothing in the cloud.
func cloudResources(r *recorder) int {
	n := 0
	for _, name := range r.names() {
		if !strings.HasPrefix(name, "truvity:audit:Audit/") {
			n++
		}
	}
	return n
}

func variables(t *testing.T, f declared) map[string]string {
	t.Helper()
	out := map[string]string{}
	for k, v := range prop(f, "environment").ObjectValue()["variables"].ObjectValue() {
		out[string(k)] = v.StringValue()
	}
	return out
}

// The function's code is the release's zip as it was published, and nothing of the
// library's is added to it.
func TestTheFunctionsCodeIsTheReleasesZipUnchanged(t *testing.T) {
	var writerZip, writerSHA string
	rec, _, err := build(t, func(a *auditpulumi.Args) { writerZip, writerSHA = a.Writer.Package, a.Writer.PackageSHA256 })
	if err != nil {
		t.Fatal(err)
	}
	f := rec.one(t, "aws:lambda/function:Function", "audit-writer")
	code := prop(f, "code")
	if !code.IsArchive() || !code.ArchiveValue().IsPath() {
		t.Fatalf("the code is not a file: %v", code)
	}
	raw, err := os.ReadFile(code.ArchiveValue().Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != writerSHA {
		t.Errorf("the code's SHA-256 is %x, the release's is %s", sum, writerSHA)
	}
	if want := base64.StdEncoding.EncodeToString(sum[:]); prop(f, "sourceCodeHash").StringValue() != want {
		t.Errorf("sourceCodeHash = %q, want what Lambda reports for the code, %q", prop(f, "sourceCodeHash").StringValue(), want)
	}
	// The same bytes as the file the release published, not a rebuilt archive.
	orig, err := os.ReadFile(writerZip)
	if err != nil {
		t.Fatal(err)
	}
	if string(orig) != string(raw) {
		t.Error("the code is not the file that was named")
	}
}

// The configuration is an immutable layer version mounted at /opt/audit/, and the
// function is told where it is.
func TestTheConfigurationIsALayerAndTheEnvironmentSaysWhere(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"audit-writer", "audit-notary"} {
		l := rec.one(t, "aws:lambda/layerVersion:LayerVersion", fn+"-config")
		if a := prop(l, "compatibleArchitectures").ArrayValue(); len(a) != 1 || a[0].StringValue() != "arm64" {
			t.Errorf("%s layer architectures: %v", fn, a)
		}
		if r := prop(l, "compatibleRuntimes").ArrayValue(); len(r) != 1 || r[0].StringValue() != "provided.al2023" {
			t.Errorf("%s layer runtimes: %v", fn, r)
		}
		if !prop(l, "skipDestroy").BoolValue() {
			t.Errorf("%s: an old layer version is deleted when it is replaced, and the function version that ran with it points at it", fn)
		}
		// Under audit/ in the zip, which a layer extracts to /opt/audit/.
		assets, _ := prop(l, "code").ArchiveValue().GetAssets()
		for name := range assets {
			if !strings.HasPrefix(name, "audit/") {
				t.Errorf("%s: %s is in the layer outside audit/", fn, name)
			}
		}
		if _, ok := assets["audit/audit.yaml"]; !ok {
			t.Errorf("%s: no audit/audit.yaml in the layer", fn)
		}
		f := rec.one(t, "aws:lambda/function:Function", fn)
		env := variables(t, f)
		if env["AUDIT_CONFIG"] != "/opt/audit/audit.yaml" {
			t.Errorf("%s: AUDIT_CONFIG = %q", fn, env["AUDIT_CONFIG"])
		}
		if !strings.Contains(env["AUDIT_CONFIG_LAYER"], fn+"-config") {
			t.Errorf("%s: AUDIT_CONFIG_LAYER = %q, want the layer version's ARN", fn, env["AUDIT_CONFIG_LAYER"])
		}
		if n := len(prop(f, "layers").ArrayValue()); n > 5 {
			t.Errorf("%s has %d layers, a function may have five", fn, n)
		}
	}
}

func TestAnEditedConfigurationIsANewLayerAndLeavesTheCodeAlone(t *testing.T) {
	code := func(window string) (string, string) {
		rec, _, err := build(t, func(a *auditpulumi.Args) { a.Writer.DedupeWindow = window })
		if err != nil {
			t.Fatal(err)
		}
		return layerFiles(t, rec, "audit-writer")["audit.yaml"], prop(rec.one(t, "aws:lambda/function:Function", "audit-writer"), "sourceCodeHash").StringValue()
	}
	cfgA, hashA := code("100h")
	cfgB, hashB := code("200h")
	if cfgA == cfgB {
		t.Error("an edited configuration left the layer unchanged")
	}
	if hashA != hashB {
		t.Error("an edited configuration changed the function's code")
	}
}

func TestAPackageIsCheckedBeforeAnythingIsCreated(t *testing.T) {
	// A checksum for the wrong file is the case the digest is for.
	for name, c := range map[string]struct {
		edit func(t *testing.T, a *auditpulumi.Args)
		says string
	}{
		"another file's digest": {func(_ *testing.T, a *auditpulumi.Args) {
			a.Writer.PackageSHA256 = strings.Repeat("a", 64)
		}, "SHA-256"},
		"no digest": {func(_ *testing.T, a *auditpulumi.Args) { a.Notary.PackageSHA256 = "" }, "PackageSHA256"},
		"a short digest": {func(_ *testing.T, a *auditpulumi.Args) {
			a.Notary.PackageSHA256 = "abc"
		}, "PackageSHA256"},
		"not a zip": {func(t *testing.T, a *auditpulumi.Args) {
			p := filepath.Join(t.TempDir(), "audit-writer-lambda_0.11.0_linux_arm64.zip")
			if err := os.WriteFile(p, []byte("not a zip"), 0o600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte("not a zip"))
			a.Writer.Package, a.Writer.PackageSHA256 = p, hex.EncodeToString(sum[:])
		}, "not a zip"},
		"no bootstrap": {func(t *testing.T, a *auditpulumi.Args) {
			a.Writer.Package, a.Writer.PackageSHA256 = zipOf(t, t.TempDir(), "audit-writer-lambda_0.11.0_linux_arm64.zip",
				map[string][]byte{"LICENSE": []byte("x")})
		}, "bootstrap"},
		"a path out of the package": {func(t *testing.T, a *auditpulumi.Args) {
			a.Writer.Package, a.Writer.PackageSHA256 = zipOf(t, t.TempDir(), "audit-writer-lambda_0.11.0_linux_arm64.zip",
				map[string][]byte{"bootstrap": []byte("x"), "../evil": []byte("x")})
		}, "outside the package root"},
		"not an executable": {func(t *testing.T, a *auditpulumi.Args) {
			a.Writer.Package, a.Writer.PackageSHA256 = zipOf(t, t.TempDir(), "audit-writer-lambda_0.11.0_linux_arm64.zip",
				map[string][]byte{"bootstrap": []byte("#!/bin/true\n")})
		}, "not an executable Linux binary"},
		"the notary's zip as the writer's": {func(_ *testing.T, a *auditpulumi.Args) {
			a.Writer.Package, a.Writer.PackageSHA256 = a.Notary.Package, a.Notary.PackageSHA256
		}, "not interchangeable"},
		"an amd64 binary": {func(t *testing.T, a *auditpulumi.Args) {
			a.Writer.Package, a.Writer.PackageSHA256 = zipOf(t, t.TempDir(), "audit-writer-lambda_0.11.0_linux_arm64.zip",
				map[string][]byte{"bootstrap": amd64Fixture(t)})
		}, "arm64"},
		"a plain http URL": {func(_ *testing.T, a *auditpulumi.Args) {
			a.Writer.Package = "http://releases.example.test/audit-writer-lambda_0.11.0_linux_arm64.zip"
		}, "https URL"},
	} {
		t.Run(name, func(t *testing.T) {
			rec, _, err := build(t, func(a *auditpulumi.Args) { c.edit(t, a) })
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("got %v, want a refusal naming %q", err, c.says)
			}
			if n := cloudResources(rec); n != 0 {
				t.Errorf("%d resources were declared before the package was refused: %v", n, rec.names())
			}
		})
	}
}

func amd64Fixture(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":                       "module github.com/truvity/audit\n\ngo 1.27\n",
		"cmd/audit-writer-lambda/m.go": "package main\n\nfunc main() {}\n",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "bootstrap")
	build := exec.Command("go", "build", "-o", out, "./cmd/audit-writer-lambda")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=")
	if msg, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, msg)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An https URL is a source like a path, held to the same digest. The test serves
// plain http on the loopback, which the library allows for exactly this.
func TestAPackageMayBeFetchedFromAURLAndIsHeldToTheSameDigest(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	var url string
	edit := func(a *auditpulumi.Args) {
		raw, err := os.ReadFile(a.Writer.Package)
		if err != nil {
			t.Fatal(err)
		}
		body = raw
		url = srv.URL + "/v0.11.0/audit-writer-lambda_0.11.0_linux_arm64.zip"
		a.Writer.Package = url
	}
	if _, _, err := build(t, edit); err != nil {
		t.Fatalf("a package from a URL: %v", err)
	}
	if _, _, err := build(t, func(a *auditpulumi.Args) {
		edit(a)
		body = append(append([]byte{}, body...), 0) // the server now serves other bytes than the digest names
	}); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("a changed download was accepted: %v", err)
	}
}

// The library renders the configuration for its own release; a binary of another
// one may refuse it at start-up, which drains the ingest queue.
func TestABinaryOfAnotherReleaseThanTheLibraryIsRefused(t *testing.T) {
	rec, _, err := build(t, func(*auditpulumi.Args) {
		t.Cleanup(auditpulumi.SetLibraryVersion("0.12.0"))
	})
	if err == nil || !strings.Contains(err.Error(), "0.11.0") || !strings.Contains(err.Error(), "0.12.0") {
		t.Fatalf("got %v, want a refusal naming both releases", err)
	}
	if n := len(rec.ofType("aws:lambda/function:Function")); n != 0 {
		t.Errorf("a function was declared with the wrong release")
	}
	if _, _, err := build(t, func(a *auditpulumi.Args) {
		t.Cleanup(auditpulumi.SetLibraryVersion("0.12.0"))
		a.Guards.AllowVersionSkew = true
	}); err != nil {
		t.Errorf("the skew was acknowledged and still refused: %v", err)
	}
	// A zip that is not named as the release names it has no release to compare.
	if _, _, err := build(t, func(a *auditpulumi.Args) {
		raw, err := os.ReadFile(a.Writer.Package)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "my-build.zip")
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		a.Writer.Package = p
	}); err == nil || !strings.Contains(err.Error(), "named as the release names") {
		t.Errorf("a zip with a name of its own was accepted: %v", err)
	}
	// With no release known for the library (a replace directive, a checkout), there is nothing to compare.
	if _, _, err := build(t, func(*auditpulumi.Args) { t.Cleanup(auditpulumi.SetLibraryVersion("")) }); err != nil {
		t.Errorf("an unknown library release was refused: %v", err)
	}
}

const appCatalogue = "source: app\nversion: \"1.0.0\"\n"

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// A catalogue the archive holds under this version with other bytes is the one
// the writer refuses to start on. It is refused here instead, before the
// function is touched.
func TestAChangedCatalogueUnderAnUnchangedVersionIsRefusedBeforeTheFunction(t *testing.T) {
	withCatalogue := func(a *auditpulumi.Args) { a.Writer.Catalogues = map[string]string{"catalogue.yaml": appCatalogue} }
	// Not in the archive yet, or a bucket that is not there yet: nothing to compare.
	if _, _, err := buildArchived(t, nil, withCatalogue, nil); err != nil {
		t.Fatalf("a new catalogue version was refused: %v", err)
	}
	// The same bytes under the same version: a redeploy.
	if _, _, err := buildArchived(t, map[string]string{"catalogue/app/1.0.0": sha(appCatalogue)}, withCatalogue, nil); err != nil {
		t.Fatalf("an unchanged catalogue was refused: %v", err)
	}
	// Other bytes under the same version.
	rec, _, err := buildArchived(t, map[string]string{"catalogue/app/1.0.0": sha("source: app\nversion: \"1.0.0\"\nactions: {}\n")}, withCatalogue, nil)
	if err == nil {
		t.Fatal("a changed catalogue under an unchanged version was accepted")
	}
	for _, want := range []string{"catalogue.yaml", "app version 1.0.0", "new version", "dead-letter"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say %q: %v", want, err)
		}
	}
	if n := cloudResources(rec); n != 0 {
		t.Errorf("%d resources were declared before the catalogue was refused: %v", n, rec.names())
	}
	// Another version beside it is not the same document.
	if _, _, err := buildArchived(t, map[string]string{"catalogue/app/0.9.0": sha("old")}, withCatalogue, nil); err != nil {
		t.Errorf("a different version was compared: %v", err)
	}
	// An object that cannot be compared is not a pass.
	if _, _, err := buildArchived(t, map[string]string{"catalogue/app/1.0.0": ""}, withCatalogue, nil); err == nil ||
		!strings.Contains(err.Error(), "no sha256 metadata") {
		t.Errorf("an archived copy with no digest was passed: %v", err)
	}
	// The switch is a decision, and says what it gives up.
	if _, _, err := buildArchived(t, map[string]string{"catalogue/app/1.0.0": sha("other")}, func(a *auditpulumi.Args) {
		withCatalogue(a)
		a.Guards.SkipCatalogueCheck = true
	}, nil); err != nil {
		t.Errorf("the comparison was skipped and still failed: %v", err)
	}
}

func TestACatalogueFromAFileIsComparedLikeOneGivenInline(t *testing.T) {
	_, _, err := buildArchived(t, map[string]string{"catalogue/app/1.0.0": sha("other")}, func(a *auditpulumi.Args) {
		a.Writer.CataloguePaths = []string{writeCatalogue(t, "catalogue.yaml", appCatalogue)}
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("got %v", err)
	}
}

func TestACatalogueWithNoVersionIsRefused(t *testing.T) {
	_, _, err := build(t, func(a *auditpulumi.Args) { a.Writer.Catalogues = map[string]string{"catalogue.yaml": "source: app\n"} })
	if err == nil || !strings.Contains(err.Error(), "no source or no version") {
		t.Fatalf("got %v", err)
	}
}

// A key that is not in the archive, and a bucket that is not there yet, are the
// provider's own not-found text; a deployer without s3:ListBucket gets a 403 and
// is told which permission is missing.
func TestTheCatalogueGuardReadsTheProvidersNotFoundAndNamesAMissingPermission(t *testing.T) {
	with := func(a *auditpulumi.Args) { a.Writer.Catalogues = map[string]string{"catalogue.yaml": appCatalogue} }
	if _, _, err := buildArchived(t, nil, with, nil); err != nil {
		t.Errorf("a first deploy, with no bucket or no object, was refused: %v", err)
	}
	if !auditpulumi.Absent(errors.New("reading S3 Bucket (b) Object (k): couldn't find resource")) {
		t.Error("the provider's not-found text is not read as not found")
	}
	if auditpulumi.Absent(errors.New("operation error S3: HeadObject, https response error StatusCode: 403, Forbidden")) {
		t.Error("a 403 was read as not found")
	}
	if !auditpulumi.Denied(errors.New("StatusCode: 403, Forbidden")) {
		t.Error("a 403 is not recognised")
	}
}

func TestASecretInTheKeysBlockIsRefusedBeforeItIsKeptInALayer(t *testing.T) {
	_, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.Keys = map[string]any{"provider": "transit", "transit": map[string]any{"openbao": map[string]any{"token": "s.abc"}}}
	})
	if err == nil || !strings.Contains(err.Error(), "looks like a secret") {
		t.Fatalf("got %v", err)
	}
	if _, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.Keys = map[string]any{"provider": "transit", "transit": map[string]any{"openbao": map[string]any{"tokenEnv": "BAO_TOKEN", "tokenFile": "/x"}}}
	}); err != nil {
		t.Errorf("a reference was refused: %v", err)
	}
}
