package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DigestBytes is `sha256:` and the hex SHA-256 of b, the form every digest in a
// record of the process's own configuration takes.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// DigestFile is the digest of one file, as `sha256sum` would print it. An empty
// path is the empty string: a configuration that names no such document has no
// digest to give, which is not the digest of nothing.
func DigestFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return DigestBytes(b), nil
}

// DigestTree is the digest of a directory of files: of each regular file's
// path relative to the directory and its digest, in path order, so that
// renaming a file changes it as much as editing one does. An empty dir is the
// empty string.
func DigestTree(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// A Kubernetes volume is a ..data link to a timestamped directory, and the
		// files are links through it: take the files by the names they are given
		// and not the machinery behind them, which would count each twice.
		if p != dir && strings.HasPrefix(d.Name(), "..") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if info, err := os.Stat(p); err != nil {
			return err
		} else if info.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		lines = append(lines, fmt.Sprintf("%s %s\n", filepath.ToSlash(rel), DigestBytes(b)))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(lines)
	var all []byte
	for _, l := range lines {
		all = append(all, l...)
	}
	return DigestBytes(all), nil
}
