#!/usr/bin/env bash
# The release gate: a release is made only from a commit that is an ancestor of master and whose CI run on master concluded
# successfully. Anything else is refused: a commit that is not on master, one that no run covers, a run that failed, was cancelled or is
# still going, and a run that cannot be read. Copied from atoom's tools/release/gate.sh, with the names of this repository.
#
#   tools/release/gate.sh <commit>
#
# Run it in a clone that has the history of master (the release workflow checks out with fetch-depth 0), with `gh` authenticated for
# the repository named by GITHUB_REPOSITORY. AUDIT_GATE_BRANCH and AUDIT_GATE_WORKFLOW name the branch and the CI workflow file
# (master and check.yaml); the release workflow sets neither.
set -euo pipefail

sha=${1:-}
[[ "$sha" =~ ^[0-9a-f]{40}$ ]] || { echo "gate: refused: '$sha' is not a full commit id" >&2; exit 2; }
repo=${GITHUB_REPOSITORY:?gate: GITHUB_REPOSITORY is not set}
branch=${AUDIT_GATE_BRANCH:-master}
workflow=${AUDIT_GATE_WORKFLOW:-check.yaml}

refuse() { echo "gate: refused: $*" >&2; exit 1; }

git cat-file -e "$sha^{commit}" 2>/dev/null || refuse "$sha is not a commit of this clone"
git rev-parse --verify --quiet "refs/remotes/origin/$branch" >/dev/null || refuse "this clone has no origin/$branch to compare with"
git merge-base --is-ancestor "$sha" "origin/$branch" || refuse "$sha is not an ancestor of $branch"

runs=$(gh api "repos/$repo/actions/workflows/$workflow/runs?head_sha=$sha&branch=$branch&event=push&per_page=50") ||
    refuse "the runs of $workflow for $sha could not be read"
# The newest run on that commit, on that branch, from a push; a re-run keeps its run id and shows its last attempt.
newest=$(jq -c --arg sha "$sha" --arg branch "$branch" '
    [.workflow_runs[]? | select(.head_sha == $sha and .head_branch == $branch and .event == "push")]
    | sort_by(.created_at) | last // empty' <<<"$runs") || refuse "the runs of $workflow for $sha could not be parsed"
[ -n "$newest" ] || refuse "no run of $workflow on $branch covers $sha"
status=$(jq -r '.status // ""' <<<"$newest")
conclusion=$(jq -r '.conclusion // ""' <<<"$newest")
[ "$status" = completed ] || refuse "the run of $workflow for $sha is $status, not completed"
[ "$conclusion" = success ] || refuse "the run of $workflow for $sha concluded $conclusion, not success"
echo "gate: $sha is an ancestor of $branch and its run of $workflow on $branch succeeded"
