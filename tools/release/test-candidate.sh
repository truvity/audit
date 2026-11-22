#!/usr/bin/env bash
# Tests of the candidate scripts on fixtures, with no build and no network (`just release-candidate-test`, part of `just check`):
# candidate-scope.sh on a throw-away repository (the heading of CHANGELOG.md decides), assemble.sh and check-dist.sh on the archive of
# HEAD, the evidence gate with candidate-mutants.sh on a directory of made-up files, and publish.sh against a stand-in for gh. The real
# candidate runs the same mutants on its own bytes. Copied from atoom's tools/release/test-candidate.sh, with the files of this repository.
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fails=0
ok() { echo "test-candidate: ok: $*"; }
no() { echo "test-candidate: FAILED: $*" >&2; fails=$((fails + 1)); }
expect() { # what want got
    if [ "$2" = "$3" ]; then ok "$1"; else no "$1: wanted '$2', got '$3'"; fi
}

# candidate-scope.sh
repo=$tmp/repo
mkdir "$repo"
g() { git -C "$repo" -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false -c tag.gpgsign=false "$@"; }
g init -q -b master
# heading TEXT NOTE: a commit whose CHANGELOG.md starts with the section TEXT (NOTE makes two commits differ when their headings do not).
heading() { printf '# Changelog\n\n%s\n\nnotes %s\n' "$1" "$2" >"$repo/CHANGELOG.md"; g add -A; g commit -q -m "$1 $2"; g rev-parse HEAD; }
scope() { (cd "$repo" && "$here/candidate-scope.sh" "$1" "$2" | tr '\n' ' '); }
a=$(heading "## Unreleased" 1)
b=$(heading "## Unreleased" 2)
c=$(heading "## 0.2.0 - 2026-11-20" 3)
d=$(heading "## 0.2.0 - 2026-11-21" 4)
expect "an unchanged heading is no candidate" "run=false reason=the head of CHANGELOG.md did not change (## Unreleased) " "$(scope "$a" "$b")"
expect "a version heading is a candidate" "run=true version=0.2.0 " "$(scope "$b" "$c")"
expect "the first push has nothing to compare" "run=false reason=the push has no commit before it to compare with " "$(scope 0000000000000000000000000000000000000000 "$c")"
g tag v0.2.0 "$c"
expect "a tagged version is no candidate" "run=false reason=v0.2.0 is already tagged " "$(scope "$b" "$d")"
e=$(heading "## Unreleased" 5)
expect "going back to Unreleased is no candidate" "run=false reason=the head of CHANGELOG.md is '## Unreleased': 'Unreleased' is not a release version (X.Y.Z) " "$(scope "$d" "$e")"
f=$(heading "## 0.3.0-rc.1" 6)
expect "a pre-release version is no candidate" "run=false reason=the head of CHANGELOG.md is '## 0.3.0-rc.1': '0.3.0-rc.1' is not a release version (X.Y.Z) " "$(scope "$e" "$f")"

# publish.sh against a stand-in for gh: the order (draft, check, publish, immutability) and the refusals
bin=$tmp/bin
mkdir "$bin" "$tmp/assets" "$tmp/dup"
cat >"$bin/gh" <<'STUB'
#!/usr/bin/env bash
# A stand-in for gh: it logs every call and answers what publish.sh asks. FAKE_IMMUTABLE and FAKE_DROP shape the answers.
echo "$*" >>"$FAKE_LOG"
case "$1 $2" in
    "release create")
        shift 2; tag=$1; shift
        : >"$FAKE_STATE"
        while [ "$#" -gt 0 ]; do
            case "$1" in --title | --notes-file) shift 2 ;; --draft | --verify-tag) shift ;; *) basename "$1" >>"$FAKE_STATE"; shift ;; esac
        done
        ;;
    "release view")
        case "$*" in
            *isDraft*) echo "true $3" ;;
            *assets*) if [ -n "${FAKE_DROP:-}" ]; then grep -v "$FAKE_DROP" "$FAKE_STATE" || true; else cat "$FAKE_STATE"; fi ;;
        esac
        ;;
    "release edit") ;;
    "api repos/"*) echo "${FAKE_IMMUTABLE:-true}" ;;
    *) echo "gh stub: unexpected $*" >&2; exit 1 ;;
esac
STUB
chmod +x "$bin/gh"
echo notes >"$tmp/notes.md"
echo a >"$tmp/assets/a"; echo b >"$tmp/assets/b"; echo c >"$tmp/dup/a"
pub() { PATH="$bin:$PATH" GITHUB_REPOSITORY=o/r FAKE_LOG="$tmp/gh.log" FAKE_STATE="$tmp/gh.state" "$@" "$here/publish.sh" v0.2.0 "$tmp/notes.md" "$tmp/assets/a" "$tmp/assets/b"; }
: >"$tmp/gh.log"
if out=$(pub env 2>&1); then ok "publish: a draft with every asset is published"; else no "publish: a good release is refused"; echo "$out" >&2; fi
create=$(grep -n '^release create' "$tmp/gh.log" | head -1 | cut -d: -f1); edit=$(grep -n '^release edit' "$tmp/gh.log" | head -1 | cut -d: -f1)
if [ -n "$create" ] && [ -n "$edit" ] && [ "$create" -lt "$edit" ] && grep -q -- '--draft' "$tmp/gh.log"; then ok "publish: the draft is created before it is published"; else no "publish: the order is not draft then publish"; fi
if out=$(pub env FAKE_IMMUTABLE=false 2>&1); then no "publish: a release that is not immutable passed"; else ok "publish: a release that is not immutable fails the job"; fi
if out=$(pub env FAKE_DROP=b 2>&1); then no "publish: a draft that lacks an asset passed"; else ok "publish: a draft that lacks an asset is refused"; fi
if out=$(PATH="$bin:$PATH" GITHUB_REPOSITORY=o/r FAKE_LOG="$tmp/gh.log" FAKE_STATE="$tmp/gh.state" "$here/publish.sh" v0.2.0 "$tmp/notes.md" "$tmp/assets/a" "$tmp/dup/a" 2>&1); then no "publish: two assets of one name passed"; else ok "publish: two assets of one name are refused"; fi
if out=$(PATH="$bin:$PATH" GITHUB_REPOSITORY=o/r FAKE_LOG="$tmp/gh.log" FAKE_STATE="$tmp/gh.state" "$here/publish.sh" 0.2.0 "$tmp/notes.md" "$tmp/assets/a" 2>&1); then no "publish: a tag without v passed"; else ok "publish: a tag that is not vX.Y.Z is refused"; fi

# assemble.sh and check-dist.sh: the archive of HEAD is exactly the release; a file added, a false sum and a missing file are refused
dist=$tmp/dist
if out=$("$here/assemble.sh" 0.0.0 "$dist" 2>&1) && out=$("$here/check-dist.sh" "$dist" 0.0.0 2>&1); then ok "check-dist: the assembled archive of HEAD is the release"; else no "check-dist: the assembled release is refused"; echo "$out" >&2; fi
if out=$("$here/check-dist.sh" "$dist" 0.0.1 2>&1); then no "check-dist: another version passed"; else ok "check-dist: another version is refused"; fi
cp -r "$dist" "$tmp/dist-extra"; echo x >"$tmp/dist-extra/extra"
if out=$("$here/check-dist.sh" "$tmp/dist-extra" 0.0.0 2>&1); then no "check-dist: an extra file passed"; else ok "check-dist: an extra file is refused"; fi
cp -r "$dist" "$tmp/dist-sum"; printf 'x' >>"$tmp/dist-sum/audit-0.0.0.tar.gz"
if out=$("$here/check-dist.sh" "$tmp/dist-sum" 0.0.0 2>&1); then no "check-dist: a changed archive passed"; else ok "check-dist: a changed archive (a false sum) is refused"; fi
cp -r "$dist" "$tmp/dist-gone"; rm "$tmp/dist-gone/SHA256SUMS"
if out=$("$here/check-dist.sh" "$tmp/dist-gone" 0.0.0 2>&1); then no "check-dist: a missing SHA256SUMS passed"; else ok "check-dist: a missing SHA256SUMS is refused"; fi

# the evidence gate, on a directory of made-up files
files=$tmp/files
mkdir "$files"
for n in one two three; do echo "$n" >"$files/$n"; done
commit=$(printf 'a%.0s' $(seq 40))
for arch in amd64 arm64; do
    "$here/evidence.sh" record "$arch" 0.2.0 "$commit" "$tmp/rec-$arch.json"
done
"$here/evidence.sh" merge "$files" 0.2.0 "$commit" true "$tmp/rec-amd64.json" "$tmp/rec-arm64.json" >"$tmp/evidence.json"
if out=$("$here/candidate-mutants.sh" "$files" "$tmp/evidence.json" 0.2.0 "$commit" 2>&1); then ok "every mutant of the evidence is refused"; else no "the mutants of the evidence"; echo "$out" >&2; fi

[ "$fails" -eq 0 ] || exit 1
echo "test-candidate: all passed"
