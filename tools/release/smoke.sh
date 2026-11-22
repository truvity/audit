#!/usr/bin/env bash
# The smoke of a candidate (docs/release.md, "The candidate"): the source archive of the release, unpacked in a clean directory, builds and
# passes its tests, the Go modules (go.work of the archive) and the Rust workspace, on the architecture this runs on. It runs the archive's
# bytes, not the checkout. Run it in the devbox shell (Go, and the Rust toolchain that `just rust-toolchain` installs).
#
#   tools/release/smoke.sh <directory> <version>
set -euo pipefail
dir_=${1:?usage: smoke.sh <directory> <version>}
version=${2:?usage: smoke.sh <directory> <version>}
dir_=$(cd "$dir_" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

tar -xzf "$dir_/audit-$version.tar.gz" -C "$work"
src=$work/audit-$version
cd "$src"
for d in $(go work edit -json | jq -r '.Use[].DiskPath'); do
    echo "smoke: go build, vet and test: $d"
    (cd "$d" && go build -o /dev/null ./... && go vet ./... && go test -count=1 ./...)
done
echo "smoke: cargo test of the workspace"
CARGO_TARGET_DIR=$work/target cargo test --workspace --locked
echo "smoke: the archive of $version builds and passes its tests on $(uname -m)"
