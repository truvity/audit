#!/usr/bin/env bash
# The post-tag proof of consumption (docs/release.md): in clean temporary projects, `go get` every Go
# library at vX.Y.Z through the public proxy and build it, and build a smoke Rust crate against the git tag.
#
#   tools/release/consume.sh X.Y.Z            run it (needs the tags pushed; the proxy may lag, so each `go get` is retried)
#   tools/release/consume.sh --list X.Y.Z     print what it would fetch, and fetch nothing
#
# Run in a clone checked out at the tag: the libraries are the tracked go.mod and Cargo.toml files of that tree.
set -euo pipefail
cd "$(dirname "$0")/../.."
root=$PWD
# shellcheck source=tools/release/common.sh
. tools/release/common.sh

list=0
if [ "${1:-}" = "--list" ]; then
    list=1
    shift
fi
version=${1:-}
valid_version "$version" || die "usage: consume.sh [--list] X.Y.Z"

libs=$(go_libraries)
crates=$(cargo metadata --no-deps --format-version 1 | jq -r '.packages[].name')

if [ "$list" -eq 1 ]; then
    for dir in $libs; do echo "go get $(module_path "$dir")@v$version"; done
    for c in $crates; do echo "cargo: $c = { git = \"https://github.com/truvity/audit\", tag = \"v$version\" }"; done
    exit 0
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Go: one clean module, a `go get` per library with retries (the proxy learns a tag within minutes), then a build of each.
mkdir "$work/gosmoke"
(
    cd "$work/gosmoke"
    export GOFLAGS=-mod=mod GOWORK=off CGO_ENABLED=0 GOPROXY=${AUDIT_CONSUME_GOPROXY:-https://proxy.golang.org}
    go mod init example.invalid/audit-smoke >/dev/null 2>&1
    for dir in $libs; do
        mod=$(module_path "$dir")
        ok=0
        for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
            if go get "$mod@v$version" >/dev/null 2>&1; then ok=1; break; fi
            echo "consume: $mod@v$version is not on the proxy yet (attempt $attempt); waiting" >&2
            sleep 30
        done
        [ "$ok" -eq 1 ] || die "go get $mod@v$version failed through the proxy"
        echo "consume: go get $mod@v$version ok"
    done
    for dir in $libs; do
        mod=$(module_path "$dir")
        go build "$mod/..." || die "go build $mod/... failed"
    done
)

# Rust: one clean crate that depends on every crate of the workspace at the tag.
mkdir -p "$work/rustsmoke/src"
{
    printf '[package]\nname = "audit-smoke"\nversion = "0.0.0"\nedition = "2024"\npublish = false\n\n[dependencies]\n'
    for c in $crates; do
        printf '%s = { git = "https://github.com/truvity/audit", tag = "v%s" }\n' "$c" "$version"
    done
} >"$work/rustsmoke/Cargo.toml"
echo 'fn main() {}' >"$work/rustsmoke/src/main.rs"
cp "$root/rust-toolchain.toml" "$work/rustsmoke/"
(cd "$work/rustsmoke" && cargo build) || die "the smoke crate did not build against the tag v$version"
echo "consume: every library of v$version builds from the public proxy and the git tag"
