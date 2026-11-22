#!/usr/bin/env bash
# The files of a release (docs/release.md, "What the release holds"): the source archive of the commit and its sums.
#
#   tools/release/assemble.sh <version> [<directory>]      (the directory defaults to dist/release)
#
# audit releases libraries only (record 0016): a Go module and a Rust crate, consumed through their tags. The release carries the source of
# the tagged commit as one archive, audit-X.Y.Z.tar.gz (`git archive` of HEAD, every path under audit-X.Y.Z/), and SHA256SUMS. These are the
# bytes that the candidate builds and tests on both architectures, that the attestation covers and that the release publishes. The archive
# is of the commit, not of the working tree: an uncommitted change is not in it.
set -euo pipefail
cd "$(dirname "$0")/../.."
# shellcheck source=tools/release/common.sh
. tools/release/common.sh

version=${1:-}
valid_version "$version" || die "usage: assemble.sh <version> [<directory>]"
out=${2:-dist/release}
rm -rf "$out"
mkdir -p "$out"
name="audit-$version.tar.gz"
git archive --format=tar --prefix="audit-$version/" HEAD | gzip -n -9 >"$out/$name"
(cd "$out" && sha256sum -- "$name" >SHA256SUMS)
echo "assemble: $out holds $name and SHA256SUMS for $(git rev-parse HEAD)"
