#!/usr/bin/env bash
# Publishes the GitHub release of a tag, in the order that immutable releases need: a DRAFT with every asset, the draft checked, then made
# public, then the published release checked for immutability (docs/release.md). It is the body of the step "Draft, check, publish" of
# .github/workflows/release.yaml, a script so that a test can run it against a stand-in for `gh`.
#
#   tools/release/publish.sh <tag> <notes-file> <asset>...
#
# Needs `gh` authenticated (GH_TOKEN) with contents: write, and GITHUB_REPOSITORY. It refuses an asset name that appears twice, a draft that
# is not a draft or does not hold exactly the assets given, and a published release that is not immutable. Immutable releases are a
# repository setting that a GITHUB_TOKEN cannot read (the settings endpoint needs Administration read), so it is checked on the result: the
# release object carries `immutable`. A false there means the setting is off and this release can still be changed: the script fails, so
# that someone checks the release by hand.
set -euo pipefail
tag=${1:?usage: publish.sh <tag> <notes-file> <asset>...}
notes=${2:?usage: publish.sh <tag> <notes-file> <asset>...}
shift 2
[ "$#" -gt 0 ] || { echo "release: refused: no asset to publish" >&2; exit 1; }
repo=${GITHUB_REPOSITORY:?release: GITHUB_REPOSITORY is not set}
[ -s "$notes" ] || { echo "release: refused: $notes is missing or empty" >&2; exit 1; }
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo "release: refused: $tag is not a release tag (vX.Y.Z)" >&2; exit 1; }

names=$(for a in "$@"; do [ -f "$a" ] || { echo "release: refused: $a is not a file" >&2; exit 1; }; basename "$a"; done | LC_ALL=C sort)
dup=$(uniq -d <<<"$names")
[ -z "$dup" ] || { echo "release: refused: more than one asset is named $(tr '\n' ' ' <<<"$dup")" >&2; exit 1; }

# A draft first: a release that is public cannot be changed once immutable, so every asset is attached before it is made public.
gh release create "$tag" --draft --verify-tag --title "audit ${tag#v}" --notes-file "$notes" "$@"
state=$(gh release view "$tag" --json isDraft,tagName --jq '"\(.isDraft) \(.tagName)"')
[ "$state" = "true $tag" ] || { echo "release: refused: the draft is '$state'" >&2; exit 1; }
have=$(gh release view "$tag" --json assets --jq '.assets[].name' | LC_ALL=C sort)
[ "$have" = "$names" ] || {
    echo "release: refused: the draft does not hold exactly the assets given; missing or extra:" >&2
    diff <(echo "$names") <(echo "$have") >&2 || true
    exit 1
}
gh release edit "$tag" --draft=false --latest
immutable=$(gh api "repos/$repo/releases/tags/$tag" --jq .immutable) ||
    { echo "release: the release $tag is published but its immutability cannot be read: check it by hand" >&2; exit 1; }
[ "$immutable" = true ] || {
    echo "release: the release $tag is published but is NOT immutable ('$immutable'): immutable releases are off for this repository; check the published release by hand" >&2
    exit 1
}
echo "release: $tag is published and immutable, with $# assets"
