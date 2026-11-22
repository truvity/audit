#!/usr/bin/env bash
# `just release-prepare X.Y.Z`: the release PR's change (docs/release.md).
#
#   tools/release/prepare.sh [--allow-dirty] X.Y.Z
#
# On a branch, it sets `workspace.package.version` and refreshes Cargo.lock for it; for every Go library it sets the `require` of each
# sibling audit module to vX.Y.Z with `go mod edit` and removes the replace lines of audit modules (never `go mod tidy`, which cannot
# resolve tags that do not exist yet); and it writes a CHANGELOG.md section that lists the libraries that changed since the last
# release. It makes no commit, no tag and no push.
set -euo pipefail
cd "$(dirname "$0")/../.."
# shellcheck source=tools/release/common.sh
. tools/release/common.sh

allow_dirty=0
if [ "${1:-}" = "--allow-dirty" ]; then
    allow_dirty=1
    shift
fi
version=${1:-}
valid_version "$version" || die "usage: prepare.sh [--allow-dirty] X.Y.Z (got '${version}')"
at_least_first "$version" || die "$version is below $first_version, the first version of this repository (record 0016)"
[ "$allow_dirty" -eq 1 ] || [ -z "$(git status --porcelain)" ] || die "the working tree is not clean"
git rev-parse --git-dir >/dev/null 2>&1 || die "not a git repository"
git rev-parse --verify --quiet "refs/tags/v$version" >/dev/null && die "the tag v$version already exists"

prev=$(previous_tag)
if [ -n "$prev" ]; then
    newest=$(printf '%s\n%s\n' "${prev#v}" "$version" | sort -V | tail -n 1)
    { [ "$newest" = "$version" ] && [ "${prev#v}" != "$version" ]; } || die "$version is not newer than the last release $prev"
fi

# 1. The Rust workspace version, and the lock file that carries it.
awk -v v="$version" '
    /^\[/ { in_pkg = ($0 == "[workspace.package]") }
    in_pkg && /^version *=/ { print "version = \"" v "\""; done = 1; next }
    { print }
    END { if (!done) exit 3 }
' Cargo.toml >Cargo.toml.new || { rm -f Cargo.toml.new; die "Cargo.toml has no version in [workspace.package]"; }
mv Cargo.toml.new Cargo.toml
cargo update --workspace --offline >/dev/null 2>&1 || cargo update --workspace >/dev/null

# 2. The Go requires between sibling modules.
libs=$(go_libraries)
for dir in $libs; do
    f="$dir/go.mod"
    json=$(go mod edit -json "$f")
    while IFS= read -r dep; do
        [ -n "$dep" ] || continue
        go mod edit -require="$dep@v$version" "$f"
    done < <(jq -r '.Require[]? | select(.Path | startswith("github.com/truvity/audit/")) | .Path' <<<"$json")
    while IFS= read -r old; do
        [ -n "$old" ] || continue
        go mod edit -dropreplace="$old" "$f"
    done < <(jq -r '.Replace[]? | select(.Old.Path | startswith("github.com/truvity/audit/")) | .Old.Path' <<<"$json")
done

# 2b. go.work resolves the siblings at the new version to their directories: the release PR requires a version whose tag does not exist
# yet, and a workspace does not look a module up that one of its replace lines names. Before the first release the siblings are required at
# v0.0.0 and go.work replaces that version the same way. These are replace lines of go.work, never of a go.mod.
if [ -f go.work ]; then
    while IFS= read -r old; do
        [ -n "$old" ] || continue
        go work edit -dropreplace="$old"
    done < <(go work edit -json | jq -r '.Replace[]? | select(.Old.Path | startswith("github.com/truvity/audit/")) | select(.Old.Version != "") | .Old.Path + "@" + .Old.Version')
    for dir in $libs; do
        go work edit -replace="$(module_path "$dir")@v$version=./$dir"
    done
fi

# 3. The changelog: which libraries changed since the last release. A library's own files are those below its directory that no
# nested library owns.
libdirs=$( { echo "$libs"; git ls-files -- '*/Cargo.toml' | sed 's#/Cargo.toml$##' | grep -v -E '^tools(/|$)'; } | sort -u | sed '/^$/d')
changed=""
if [ -n "$prev" ]; then
    for dir in $libdirs; do
        args=("$dir")
        for other in $libdirs; do
            case "$other" in "$dir"/*) args+=(":(exclude)$other") ;; esac
        done
        if [ -n "$(git diff --name-only "$prev" HEAD -- "${args[@]}")" ] || ! git cat-file -e "$prev:$dir" 2>/dev/null; then
            changed="$changed
- \`$dir\`"
        fi
    done
    [ -n "$changed" ] || changed="
- no library changed (the release re-tags every library at the new version)"
    note="Libraries that changed since $prev:$changed"
else
    # shellcheck disable=SC2016 # the backticks are Markdown
    note="First release. Every library is new:$(for d in $libdirs; do printf '\n- `%s`' "$d"; done)"
fi
marker='<!-- release-prepare: new sections go below this line -->'
[ -f CHANGELOG.md ] || die "CHANGELOG.md is missing"
grep -qxF "$marker" CHANGELOG.md || die "CHANGELOG.md has no marker line: $marker"
if grep -q "^## $version " CHANGELOG.md; then die "CHANGELOG.md already has a section for $version"; fi
section="## $version - $(date -u +%Y-%m-%d)

One version for the whole repository: every library is tagged $version, changed or not.

$note
"
awk -v marker="$marker" -v section="$section" '{ print } $0 == marker { print ""; print section }' CHANGELOG.md >CHANGELOG.md.new
mv CHANGELOG.md.new CHANGELOG.md

echo "release-prepare: $version"
echo "  Cargo.toml workspace.package.version = $version"
for dir in $libs; do echo "  $dir/go.mod: sibling requires at v$version, no replace of an audit module"; done
echo "  CHANGELOG.md: section $version"
echo "Next: open the release PR; after the rebase merge run \`just release-tag $version\` on the merge commit."
