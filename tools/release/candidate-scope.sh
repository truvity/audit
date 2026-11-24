#!/usr/bin/env bash
# Is a push to master a release candidate? It is when the push changed the first section heading of CHANGELOG.md from `## Unreleased` (or
# from the section of an earlier release) to a section named for a release version that has no tag yet: `just release-prepare` writes that
# heading, and the pull request that merges it is this repository's own procedure for a release. Copied from atoom's
# tools/release/candidate-scope.sh, with the names of this repository.
#
#   tools/release/candidate-scope.sh <commit before the push> <commit after the push>
#
# Prints, for the workflow to add to $GITHUB_OUTPUT, `run=true` and `version=<version>` for a candidate and `run=false` and a `reason=`
# otherwise. Run it in a clone with the history and the tags. A push of several commits is compared as a whole: the head before it against
# the head after it. It never fails for a push that is not a candidate; it fails when it cannot read.
set -euo pipefail
before=${1:?usage: candidate-scope.sh <before> <after>}
after=${2:?usage: candidate-scope.sh <before> <after>}
tagre='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'

no() { echo "run=false"; echo "reason=$*"; exit 0; }

# The first section heading of CHANGELOG.md at a commit.
# A commit with no CHANGELOG.md has no heading: `git show` fails there, and under pipefail that must not end the script.
head_of() { { git show "$1:CHANGELOG.md" 2>/dev/null || true; } | awk '/^## / && !h { print; h = 1 }'; }

git cat-file -e "$after^{commit}" 2>/dev/null || { echo "candidate-scope: $after is not a commit of this clone" >&2; exit 1; }
[[ "$before" =~ ^0+$ ]] && no "the push has no commit before it to compare with"
git cat-file -e "$before^{commit}" 2>/dev/null || no "the commit before the push, $before, is not in this clone"
new=$(head_of "$after")
old=$(head_of "$before")
[ -n "$new" ] || no "CHANGELOG.md has no section"
[ "$new" != "$old" ] || no "the head of CHANGELOG.md did not change ($new)"
# `## 0.2.0`, or `## 0.2.0 - 2026-11-20`, or `## [0.2.0]`.
if [[ "$new" =~ ^##\ \[?([^] ]+)\]?(\ .*)?$ ]]; then
    version=${BASH_REMATCH[1]}
else
    no "the head of CHANGELOG.md is '$new', not a release version"
fi
[[ "$version" =~ $tagre ]] || no "the head of CHANGELOG.md is '$new': '$version' is not a release version (X.Y.Z)"
if git rev-parse -q --verify "refs/tags/v$version" >/dev/null; then
    no "v$version is already tagged"
fi
echo "run=true"
echo "version=$version"
