#!/usr/bin/env bash
# `just release-tag X.Y.Z`: the tags of one release (docs/release.md).
#
#   tools/release/tag.sh [--dry-run [--allow-off-master]] X.Y.Z
#
# One root tag vX.Y.Z and one tag <dir>/vX.Y.Z for every Go library, all or none, each signed (git tag -s) by the maintainer by hand,
# on the merge commit of the release PR. A tag is never moved; a wrong set is repaired with the next patch version. The script
# refuses unless: the working tree is clean; HEAD is origin/master; the Cargo workspace version, every sibling require and the
# changelog say X.Y.Z (tree_problems in common.sh); no go.mod replaces an audit module; no tag of the set exists, here or on origin.
#
# --dry-run runs every check and prints the tags that it would make, and makes none. --allow-off-master (dry run only) lets the
# check "HEAD is origin/master" fail with a warning, which the rehearsal needs: its commit is not on master.
#
# The tags are signed with `git tag -s`; the environment variable AUDIT_TAG_SIGN replaces that flag (the rehearsal sets -a, which
# needs no key). It never pushes. It prints the two pushes, in the order that matters: the library tags first, then the root tag alone, because
# GitHub starts no workflow for a push of more than three tags, and the release workflow starts on the root tag.
set -euo pipefail
cd "$(dirname "$0")/../.."
# shellcheck source=tools/release/common.sh
. tools/release/common.sh

dry=0
off_master=0
while [ "$#" -gt 0 ]; do
    case "$1" in
        --dry-run) dry=1; shift ;;
        --allow-off-master) off_master=1; shift ;;
        -*) die "unknown flag $1" ;;
        *) break ;;
    esac
done
version=${1:-}
valid_version "$version" || die "usage: tag.sh [--dry-run [--allow-off-master]] X.Y.Z (got '${version}')"
at_least_first "$version" || die "$version is below $first_version, the first version of this repository (record 0016)"
[ "$off_master" -eq 0 ] || [ "$dry" -eq 1 ] || die "--allow-off-master is for a dry run only"

problems=0
refuse() { echo "release-tag: refused: $*" >&2; problems=$((problems + 1)); }

[ -z "$(git status --porcelain)" ] || refuse "the working tree is not clean"

head=$(git rev-parse HEAD)
if git rev-parse --verify --quiet refs/remotes/origin/master >/dev/null; then
    if [ "$head" != "$(git rev-parse refs/remotes/origin/master)" ]; then
        if [ "$off_master" -eq 1 ]; then
            echo "release-tag: warning: HEAD is not origin/master (allowed for the rehearsal)" >&2
        else
            refuse "HEAD is not origin/master; tag the merge commit of the release PR"
        fi
    fi
else
    [ "$off_master" -eq 1 ] || refuse "this clone has no origin/master to compare with"
fi

libs=$(go_libraries)
while IFS= read -r problem; do
    [ -z "$problem" ] || refuse "$problem"
done < <(tree_problems "$version")

tags=("v$version")
for dir in $libs; do tags+=("$dir/v$version"); done
remote_tags=$(git ls-remote --tags origin 2>/dev/null || true)
for t in "${tags[@]}"; do
    git rev-parse --verify --quiet "refs/tags/$t" >/dev/null && refuse "the tag $t exists in this clone"
    grep -q "refs/tags/$t\$" <<<"$remote_tags" && refuse "the tag $t exists on origin"
done

[ "$problems" -eq 0 ] || exit 1

if [ "$dry" -eq 1 ]; then
    echo "release-tag (dry run): every check passed; on $head it would make ${#tags[@]} signed tags:"
    printf '  %s\n' "${tags[@]}"
    exit 0
fi

made=()
rollback() {
    for t in "${made[@]}"; do git tag -d "$t" >/dev/null; done
    echo "release-tag: a tag failed; the tags made so far were deleted: nothing was made" >&2
}
trap rollback ERR
for t in "${tags[@]}"; do
    # shellcheck disable=SC2086 # AUDIT_TAG_SIGN is one flag: -s (the default) signs; the rehearsal sets -a, which needs no key
    git tag ${AUDIT_TAG_SIGN:--s} -m "audit $version" "$t" "$head"
    made+=("$t")
done
trap - ERR
echo "release-tag: made ${#tags[@]} signed tags on $head. Push them yourself, the library tags first and the root tag alone last:"
echo "  git push origin $(printf 'refs/tags/%s ' "${tags[@]:1}")"
echo "  git push origin refs/tags/v$version"
