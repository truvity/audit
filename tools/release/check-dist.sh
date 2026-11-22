#!/usr/bin/env bash
# Holds the directory of a release (dist/release) to what a release is (docs/release.md, "What the release holds"); the candidate and the
# tag run both run it:
#
#   tools/release/check-dist.sh <directory> <version>
#
# EXACTLY these files, no more and no fewer: audit-X.Y.Z.tar.gz and SHA256SUMS. SHA256SUMS names the archive and nothing else, and its sum
# is true. Every path of the archive is below audit-X.Y.Z/, and it holds the files that a consumer and the smoke need: the workspaces, the
# manifests of the Go module and the Rust crate, the licence and the changelog.
set -euo pipefail
dir_=${1:?usage: check-dist.sh <directory> <version>}
version=${2:?usage: check-dist.sh <directory> <version>}
[ -d "$dir_" ] || { echo "check-dist: $dir_ is not a directory" >&2; exit 1; }

fail=0
bad() { echo "check-dist: $*" >&2; fail=1; }

name="audit-$version.tar.gz"
want=$(printf '%s\n' SHA256SUMS "$name" | LC_ALL=C sort)
have=$(find "$dir_" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort)
[ "$have" = "$want" ] || bad "the directory holds '$(tr '\n' ' ' <<<"$have")', want exactly '$(tr '\n' ' ' <<<"$want")'"

if [ -f "$dir_/SHA256SUMS" ] && [ -f "$dir_/$name" ]; then
    listed=$(awk '{ print $2 }' "$dir_/SHA256SUMS")
    [ "$listed" = "$name" ] || bad "SHA256SUMS names '$(tr '\n' ' ' <<<"$listed")', want exactly $name"
    (cd "$dir_" && sha256sum --quiet --strict -c SHA256SUMS) >/dev/null 2>&1 || bad "a sum of SHA256SUMS is not true"
    if entries=$(tar -tzf "$dir_/$name" 2>/dev/null); then
        outside=$(grep -v -E "^audit-$version/" <<<"$entries" || true)
        [ -z "$outside" ] || bad "the archive has paths outside audit-$version/: $(head -n 3 <<<"$outside" | tr '\n' ' ')"
        for f in Cargo.toml Cargo.lock rust-toolchain.toml go.work LICENSE CHANGELOG.md emitter/Cargo.toml emitter/go.mod emitter/go.sum; do
            grep -qxF "audit-$version/$f" <<<"$entries" || bad "the archive has no $f"
        done
    else
        bad "$name is not a gzip tar archive"
    fi
fi
[ "$fail" -eq 0 ] || exit 1
echo "check-dist: $dir_ is exactly the release of $version"
