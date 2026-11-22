#!/usr/bin/env bash
# The tree is prepared for a release of this version (`just release-prepare`): the Cargo workspace version, the requires between the Go
# libraries and the changelog say it, and no go.mod replaces an audit module. The candidate runs it before it builds a version it has not
# built before (tree_problems of tools/release/common.sh is the one rule; prepare.sh and tag.sh use it too).
#
#   tools/release/check-tree.sh <version>
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# shellcheck source=tools/release/common.sh
. tools/release/common.sh
version=${1:?usage: check-tree.sh <version>}
valid_version "$version" || die "'$version' is not a version (X.Y.Z)"
problems=$(tree_problems "$version")
[ -z "$problems" ] || { echo "$problems" >&2; exit 1; }
echo "check-tree: the tree is prepared for $version"
