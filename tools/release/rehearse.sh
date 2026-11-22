#!/usr/bin/env bash
# The rehearsal of the release flow (docs/release.md, "The rehearsals"): `just release-prepare`, `just release-tag --dry-run`, its refusals,
# a real run with unsigned tags, the tag verification and the consumption check as a listing, on a throw-away copy of the working tree with
# fixture libraries (fixture and fixture/sub, two Go modules that require each other and the emitter) added. It makes no tag in this
# repository and pushes nothing. It needs the network only when `cargo update` of release-prepare finds no cached copy of a git dependency. Copied from atoom's tools/release/rehearse.sh, with the names of this repository; atoom's
# isolation check of the drivers has no counterpart here.
#
#   tools/release/rehearse.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

# The version that is rehearsed: one that no real release is (a Go major of 0 or 1 is required), so that the rehearsal passes before and
# after a real release. `cur` is the version the tree says.
rv=0.99.99
cur=$(awk '/^\[/ { in_pkg = ($0 == "[workspace.package]") } in_pkg && /^version *=/ { gsub(/[" ]/, "", $3); print $3 }' Cargo.toml)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
repo=$tmp/repo
mkdir "$repo"

fails=0
ok() { echo "rehearsal: ok: $*"; }
no() { echo "rehearsal: FAILED: $*" >&2; fails=$((fails + 1)); }
# expect_fail "what" command...: the command must fail; its output is kept for the next assertion in $out.
expect_fail() {
    local what=$1
    shift
    if out=$("$@" 2>&1); then no "$what: the command succeeded, it had to refuse"; echo "$out" >&2; else ok "$what: refused"; fi
}
expect_ok() {
    local what=$1
    shift
    if out=$("$@" 2>&1); then ok "$what"; else no "$what"; echo "$out" >&2; fi
}
contains() { # what text pattern
    if grep -qF -- "$3" <<<"$2"; then ok "$1"; else no "$1: no '$3' in:"; echo "$2" >&2; fi
}
lacks() {
    if grep -qF -- "$3" <<<"$2"; then no "$1: found '$3' in:"; echo "$2" >&2; else ok "$1"; fi
}

# The copy: every tracked or new file of the working tree, in a repository of its own with one commit, tagged as the last release.
git ls-files -z --cached --others --exclude-standard | tar --null -T - -cf - | tar -xf - -C "$repo"
git_() { git -C "$repo" -c user.name=rehearsal -c user.email=rehearsal@example.invalid -c commit.gpgsign=false -c tag.gpgsign=false "$@"; }
git_ init -q -b master
git_ add -A
git_ commit -q -m "base"
git_ tag v0.18.0

# The fixture libraries, as the real ones are: siblings require each other at the last release and carry replace lines.
mkdir -p "$repo/fixture/sub"
gomod() { # dir requires... (replace lines for every sibling)
    local dir=$1 mod=github.com/truvity/audit/$1
    shift
    {
        printf 'module %s\n\ngo 1.27.0\n\ntoolchain go1.27.4\n\n' "$mod"
        for dep in "$@"; do printf 'require github.com/truvity/audit/%s v0.18.0\n' "$dep"; done
        for dep in "$@"; do
            up=..
            rest=${dir#*/}
            [ "$rest" = "$dir" ] || up=../..
            rel=$up/$dep
            printf 'replace github.com/truvity/audit/%s => %s\n' "$dep" "$rel"
        done
    } >"$repo/$dir/go.mod"
}
gomod fixture emitter
gomod fixture/sub fixture emitter
(cd "$repo" && go work edit -use ./fixture -use ./fixture/sub)
git_ add -A
git_ commit -q -m "fixture libraries"
# fixture/sub changes after the last release; the emitter does not.
echo "// changed" >"$repo/fixture/sub/doc.go"
git_ add -A
git_ commit -q -m "change fixture/sub"

run_in_repo() { (cd "$repo" && "$@"); }

# 1. A tree that is not prepared is refused: the version, the requires, the replaces and the changelog all disagree.
expect_fail "tag dry run on an unprepared tree" run_in_repo tools/release/tag.sh --dry-run --allow-off-master ${rv}
contains "names the Cargo version" "$out" "workspace.package.version is '$cur'"
contains "names the old sibling require" "$out" "requires a sibling at another version"
contains "names the replace" "$out" "replaces an audit module"
contains "names the changelog" "$out" "CHANGELOG.md has no section for ${rv}"

# 2. release-prepare.
expect_fail "prepare refuses a version that is not newer" run_in_repo tools/release/prepare.sh 0.17.5
contains "names the last release" "$out" "is not newer than the last release v0.18.0"
expect_fail "prepare refuses a version below the first" run_in_repo tools/release/prepare.sh 0.16.9
contains "names the first version" "$out" "is below 0.17.0"
expect_fail "prepare refuses a malformed version" run_in_repo tools/release/prepare.sh 1.0
expect_ok "prepare ${rv}" run_in_repo tools/release/prepare.sh ${rv}
contains "the workspace version" "$(cat "$repo/Cargo.toml")" "version = \"${rv}\""
contains "the lock file follows" "$(grep -A1 'name = "audit-emitter"' "$repo/Cargo.lock")" "version = \"${rv}\""
sw=$(cat "$repo/fixture/sub/go.mod")
contains "fixture/sub requires fixture at the new version" "$sw" "github.com/truvity/audit/fixture v${rv}"
contains "fixture/sub requires the emitter at the new version" "$sw" "github.com/truvity/audit/emitter v${rv}"
lacks "fixture/sub has no replace" "$sw" "replace"
lacks "fixture has no replace" "$(cat "$repo/fixture/go.mod")" "replace"
gw=$(cat "$repo/go.work")
contains "go.work replaces fixture at the new version" "$gw" "github.com/truvity/audit/fixture v${rv} => ./fixture"
contains "go.work replaces fixture/sub at the new version" "$gw" "github.com/truvity/audit/fixture/sub v${rv} => ./fixture/sub"
contains "go.work replaces the emitter at the new version" "$gw" "github.com/truvity/audit/emitter v${rv} => ./emitter"
lacks "go.work keeps no replace at the placeholder version" "$gw" "v0.0.0 =>"
# Only the new section: the sections of the real releases above it name libraries too.
cl=$(awk -v h="## ${rv} - " 'index($0, h) == 1 { on = 1; print; next } /^## / { on = 0 } on' "$repo/CHANGELOG.md")
contains "the changelog has the section" "$cl" "## ${rv} - "
contains "the changelog names the changed library" "$cl" "- \`fixture/sub\`"
lacks "the changelog leaves the unchanged library out" "$cl" "- \`emitter\`"

# 3. release-tag as a dry run: a dirty tree is refused, a committed one passes and lists every tag.
expect_fail "tag dry run on a dirty tree" run_in_repo tools/release/tag.sh --dry-run --allow-off-master ${rv}
contains "names the dirty tree" "$out" "the working tree is not clean"
git_ add -A
git_ commit -q -m "release ${rv}"
expect_ok "tag dry run on the prepared tree" run_in_repo tools/release/tag.sh --dry-run --allow-off-master ${rv}
for t in v${rv} emitter/v${rv} fixture/v${rv} fixture/sub/v${rv}; do contains "the set has $t" "$out" "  $t"; done
lacks "the tooling is not tagged" "$out" "tools/v${rv}"
expect_fail "tag dry run off master without the flag" run_in_repo tools/release/tag.sh --dry-run ${rv}
git_ tag fixture/v${rv}
expect_fail "tag dry run with an existing tag" run_in_repo tools/release/tag.sh --dry-run --allow-off-master ${rv}
contains "names the existing tag" "$out" "the tag fixture/v${rv} exists"
git_ tag -d fixture/v${rv} >/dev/null
expect_fail "a real run off master" run_in_repo tools/release/tag.sh ${rv}
if [ -z "$(git_ tag --list "v${rv}" "*/v${rv}")" ]; then ok "no tag was made"; else no "a tag was made"; fi

# 3b. On master (a bare repository of its own plays origin): the real run makes every tag, annotated here because the rehearsal has no
# signing key, and the set verifies; a missing or misplaced tag is refused.
git init -q --bare -b master "$tmp/origin.git"
git_ remote add origin "$tmp/origin.git"
git_ push -q origin master
expect_ok "tag dry run on master" run_in_repo tools/release/tag.sh --dry-run ${rv}
tag_for_real() {
    (cd "$repo" && GIT_COMMITTER_NAME=rehearsal GIT_COMMITTER_EMAIL=rehearsal@example.invalid AUDIT_TAG_SIGN=-a tools/release/tag.sh "$@")
}
expect_ok "tag on master" tag_for_real ${rv}
# The library push lists every library tag followed by a space.
contains "it names the library pushes first" "$out" "refs/tags/emitter/v${rv} "
contains "it names the root push last" "$out" "git push origin refs/tags/v${rv}"
# The root tag and one per Go library of the tree (the fixtures and the libraries that have really moved; the tooling is not one).
want=$(( 1 + $(cd "$repo" && go work edit -json | jq '[.Use[].DiskPath | select(. != "./tools")] | length') ))
if [ "$(git_ tag --list "v${rv}" "*/v${rv}" | wc -l)" -eq "$want" ]; then ok "$want tags were made"; else no "the set is not $want tags"; fi
expect_ok "verify-tags on the set" run_in_repo tools/release/verify-tags.sh v${rv}
git_ tag -d emitter/v${rv} >/dev/null
expect_fail "verify-tags with a missing tag" run_in_repo tools/release/verify-tags.sh v${rv}
contains "names the missing tag" "$out" "the tag emitter/v${rv} is missing"
expect_fail "tag again once a tag exists" run_in_repo tools/release/tag.sh --dry-run ${rv}
git_ tag --list "v${rv}" "*/v${rv}" | while read -r t; do git_ tag -d "$t" >/dev/null; done

# 4. The consumption check, as a listing.
expect_ok "consume lists what it fetches" run_in_repo tools/release/consume.sh --list ${rv}
contains "the listing has the Go emitter" "$out" "go get github.com/truvity/audit/emitter@v${rv}"
contains "the listing has the Rust crate" "$out" "audit-emitter = { git = \"https://github.com/truvity/audit\", tag = \"v${rv}\" }"

if [ "$fails" -ne 0 ]; then
    echo "rehearsal: $fails assertion(s) failed" >&2
    exit 1
fi
echo "rehearsal: the release flow behaves as planned"
