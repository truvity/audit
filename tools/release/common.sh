#!/usr/bin/env bash
# Shared helpers of the release scripts (docs/release.md). Sourced, never run. Copied from atoom's tools/release/common.sh, with the names of
# this repository.
# shellcheck shell=bash

# The version in the form the maintainer types it: X.Y.Z, no leading zeros, no "v".
valid_version() {
    [[ "$1" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
}

# The directories of the Go libraries, one per line: every tracked go.mod below the root, except the tooling (tools/). The root of
# the repository is not a module.
go_libraries() {
    git ls-files -- '*/go.mod' | sed 's#/go.mod$##' | grep -v -E '^tools(/|$)' | sort || true
}

# The newest root release tag, or nothing.
previous_tag() {
    git tag --list 'v[0-9]*' --sort=-v:refname | head -n 1
}

# The module path of a Go library directory.
module_path() {
    echo "github.com/truvity/audit/$1"
}

# The lowest version this repository releases: the Go tags start at v0.17.0, above the tags of the archived module (record 0016).
first_version=0.17.0

# Is version $1 at least the first version?
at_least_first() {
    [ "$(printf '%s\n%s\n' "$first_version" "$1" | sort -V | head -n 1)" = "$first_version" ]
}

die() {
    echo "release: $*" >&2
    exit 1
}

# The problems of the tree as a release of version $1: one line each, nothing when the tree is ready. The Cargo workspace version, the
# requires between sibling Go libraries and the changelog must say $1, and no go.mod may replace an audit module.
tree_problems() {
    local version=$1 got dir json bad rep
    got=$(awk '/^\[/ { in_pkg = ($0 == "[workspace.package]") } in_pkg && /^version *=/ { gsub(/[" ]/, "", $3); print $3 }' Cargo.toml)
    [ "$got" = "$version" ] || echo "Cargo.toml workspace.package.version is '$got', not $version (run release-prepare)"
    for dir in $(go_libraries); do
        json=$(go mod edit -json "$dir/go.mod")
        bad=$(jq -r --arg v "v$version" '.Require[]? | select(.Path | startswith("github.com/truvity/audit/")) | select(.Version != $v) | .Path + " " + .Version' <<<"$json")
        [ -z "$bad" ] || echo "$dir/go.mod requires a sibling at another version than v$version: $(tr '\n' ';' <<<"$bad")"
        rep=$(jq -r '.Replace[]? | select(.Old.Path | startswith("github.com/truvity/audit/")) | .Old.Path' <<<"$json")
        [ -z "$rep" ] || echo "$dir/go.mod replaces an audit module: $(tr '\n' ' ' <<<"$rep")"
    done
    grep -q "^## $version " CHANGELOG.md || echo "CHANGELOG.md has no section for $version"
}
