#!/usr/bin/env bash
# The gate must bite (the candidate runs this on its own bytes, as atoom's candidate-mutants.sh does): the evidence is held to the files with
# `evidence.sh verify`, and each of these changes must be refused by it. The positive control first: with the rehearsal flag cleared the
# unchanged files and evidence pass.
#
#   tools/release/candidate-mutants.sh <directory> <evidence.json> <version> <commit>
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
dir=${1:?usage: candidate-mutants.sh <directory> <evidence.json> <version> <commit>}
ev=${2:?}
version=${3:?}
commit=${4:?}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

verify() { "$here/evidence.sh" verify "$@" >/dev/null 2>&1; }
must_refuse() { # what directory evidence commit
    if verify "$2" "$3" "$version" "$4"; then echo "candidate-mutants: NOT refused: $1" >&2; exit 1; fi
    echo "candidate-mutants: refused: $1"
}

jq '.rehearsal = false' "$ev" >"$work/real.json"
verify "$dir" "$work/real.json" "$version" "$commit" || { echo "candidate-mutants: the positive control failed: the unchanged candidate is refused" >&2; "$here/evidence.sh" verify "$dir" "$work/real.json" "$version" "$commit" >&2; exit 1; }
echo "candidate-mutants: the unchanged candidate passes"

first=$(find "$dir" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort | head -n 1)
mkdir "$work/flip"; cp -r "$dir"/. "$work/flip/"
printf 'x' >>"$work/flip/$first"
must_refuse "a byte added to $first" "$work/flip" "$work/real.json" "$commit"

mkdir "$work/extra"; cp -r "$dir"/. "$work/extra/"
echo extra >"$work/extra/extra-file"
must_refuse "a file added" "$work/extra" "$work/real.json" "$commit"

mkdir "$work/gone"; cp -r "$dir"/. "$work/gone/"
rm "$work/gone/$first"
must_refuse "$first taken away" "$work/gone" "$work/real.json" "$commit"

must_refuse "another commit" "$dir" "$work/real.json" "0000000000000000000000000000000000000000"
# The flag set on purpose: on a real candidate (a push to master) the evidence already says `rehearsal: false`, so passing "$ev" as it
# came would test nothing and pass the gate (atoom's 0.2.0 candidate failed here).
jq '.rehearsal = true' "$work/real.json" >"$work/rehearsal.json"
must_refuse "the evidence of a rehearsal" "$dir" "$work/rehearsal.json" "$commit"
arch=$(jq -r '.smoke | keys[0]' "$work/real.json")
jq --arg a "$arch" 'del(.smoke[$a])' "$work/real.json" >"$work/nosmoke.json"
must_refuse "no smoke of $arch" "$dir" "$work/nosmoke.json" "$commit"
jq --arg a "$arch" '.smoke[$a].smokes |= .[1:]' "$work/real.json" >"$work/fewer.json"
must_refuse "a smoke left out of the record of $arch" "$dir" "$work/fewer.json" "$commit"
echo "candidate-mutants: every change was refused"
