package auditpulumi_test

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// releaseVersion is the release the fixture binaries say they are, and the one
// the tests tell the library it is.
const releaseVersion = "0.11.0"

// A release zip as the release makes one: `bootstrap` at the root, a static
// linux/arm64 Go binary, in a file named as the release names it. The binary is
// built here from a program of the same module path and command name, so that
// the library reads the architecture and the command from the binary and not
// from anything the test hands it.
var (
	fixtureMu    sync.Mutex
	fixtureCache = map[string][]byte{}
)

func fixtureBinary(t *testing.T, cmd string) []byte {
	t.Helper()
	key := cmd
	fixtureMu.Lock()
	defer fixtureMu.Unlock()
	if b, ok := fixtureCache[key]; ok {
		return b
	}
	dir := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module github.com/truvity/audit\n\ngo 1.27\n")
	write("internal/buildinfo/buildinfo.go", "package buildinfo\n\nvar Version = \"dev\"\n")
	write("cmd/"+cmd+"/main.go", "package main\n\nimport \"github.com/truvity/audit/internal/buildinfo\"\n\nfunc main() { println(buildinfo.Version) }\n")
	out := filepath.Join(dir, "bootstrap")
	// The release's own flags: they are the ones that leave the version out of the
	// binary's build information, which is why the library reads the file's name.
	build := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", out, "./cmd/"+cmd)
	build.Dir = dir
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=")
	if msg, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the fixture binary: %v\n%s", err, msg)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	fixtureCache[key] = b
	return b
}

// releaseZip writes a release zip of cmd named for version, and
// returns its path and the SHA-256 the release's checksums would list.
func releaseZip(t *testing.T, dir, cmd, version string) (path, sha string) {
	t.Helper()
	return zipOf(t, dir, cmd+"_"+version+"_linux_arm64.zip", map[string][]byte{"bootstrap": fixtureBinary(t, cmd)})
}

func zipOf(t *testing.T, dir, name string, files map[string][]byte) (path, sha string) {
	t.Helper()
	path = filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for n, body := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return path, hex.EncodeToString(sum[:])
}
