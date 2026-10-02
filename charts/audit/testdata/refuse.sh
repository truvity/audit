#!/usr/bin/env bash
# Every file in tests/invalid/audit/ is a values overlay that must make
# `helm template` fail with the words its first line names:
#
#   # refuse: <words the refusal must contain>
#
# They are laid over a valid install (testdata/values/direct.yaml), or over
# testdata/values/<name>.yaml when the second line says `# base: <name>`. A chart that
# renders a manifest its own image will reject moves the failure from `helm
# install` to a CrashLoopBackOff, or to a trail that looks fine and is not; and
# a configuration the schema of its binary refuses has to be refused here, by
# the same words, before it is ever deployed.
set -uo pipefail
chart="$(dirname "$0")/.."
base="$chart/testdata/values/direct.yaml"
fail=0
cases=0
for overlay in tests/invalid/audit/*.yaml; do
    want=$(sed -n '1s/^# refuse: //p' "$overlay")
    if [ -z "$want" ]; then
        echo "NO EXPECTATION: $overlay has no '# refuse:' first line"
        fail=1
        continue
    fi
    cases=$((cases + 1))
    use="$base"
    if named=$(sed -n '2s/^# base: //p' "$overlay") && [ -n "$named" ]; then
        use="$chart/testdata/values/$named.yaml"
    fi
    out=$(helm template t "$chart" -f "$use" -f "$overlay" 2>&1)
    if [ $? -eq 0 ]; then
        echo "NOT REFUSED: $overlay"
        fail=1
    elif ! grep -qF -- "$want" <<< "$out"; then
        echo "REFUSED WITHOUT SAYING WHY: $overlay"
        echo "  wanted the words: $want"
        echo "  said: $(tail -3 <<< "$out" | tr '\n' ' ')"
        fail=1
    fi
done
# A guard that refuses an empty sweep: no cases is not "every refusal holds".
if [ "$cases" -eq 0 ]; then
    echo "no refusal cases found under tests/invalid/audit"
    exit 1
fi
if [ "$fail" -eq 0 ]; then
    echo "every refusal holds ($cases cases)"
fi
exit "$fail"
