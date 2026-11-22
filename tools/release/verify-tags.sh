#!/usr/bin/env bash
# The release workflow's check of the tags (docs/release.md): the tag vX.Y.Z names this commit, the tree at
# it is a prepared release of X.Y.Z (the Cargo version, the sibling requires, the changelog, no replace), and every Go library has its
# tag <dir>/vX.Y.Z on the same commit. A missing or misplaced tag refuses the release; a wrong set is repaired with the next patch version.
#
#   tools/release/verify-tags.sh vX.Y.Z
#
# Run in a clone that fetched the tags, checked out at the commit of the tag.
set -euo pipefail
cd "$(dirname "$0")/../.."
# shellcheck source=tools/release/common.sh
. tools/release/common.sh

tag=${1:-}
version=${tag#v}
if [[ "$tag" != v* ]] || ! valid_version "$version"; then die "'$tag' is not a release tag (vX.Y.Z)"; fi

problems=0
refuse() { echo "verify-tags: refused: $*" >&2; problems=$((problems + 1)); }

head=$(git rev-parse HEAD)
root=$(git rev-parse --verify --quiet "refs/tags/$tag^{commit}") || die "the tag $tag is not in this clone (fetch the tags)"
[ "$root" = "$head" ] || refuse "the tag $tag names $root, not the checked-out commit $head"
while IFS= read -r problem; do
    [ -z "$problem" ] || refuse "$problem"
done < <(tree_problems "$version")
for dir in $(go_libraries); do
    t="$dir/$tag"
    at=$(git rev-parse --verify --quiet "refs/tags/$t^{commit}") || { refuse "the tag $t is missing"; continue; }
    [ "$at" = "$head" ] || refuse "the tag $t names $at, not $head"
done
[ "$problems" -eq 0 ] || exit 1
echo "verify-tags: $tag and a tag for every Go library name $head, and the tree is the release of $version"
