#!/usr/bin/env bash
# Every line of refusals.txt must make `helm template` fail with the words it
# names. A chart that renders a manifest its own image will reject moves the
# failure from `helm install` to a CrashLoopBackOff, or to a trail that looks
# fine and is not.
set -uo pipefail
chart="$(dirname "$0")/.."
base=(--set bucket=b
      --set keys.local.existingSecret=k
      --set jobs.digest.signingKey.existingSecret=s
      --set jobs.verify.publicKey.existingSecret=p
      --set jobs.clockSync.ntp={time.example}
      --set jobs.purge.enabled=false
      --set anonymousWrites=true)
fail=0
while IFS=$'\t' read -r overrides want; do
    case "$overrides" in ''|'#'*) continue ;; esac
    read -r -a extra <<< "$overrides"
    out=$(helm template t "$chart" "${base[@]}" "${extra[@]}" 2>&1)
    if [ $? -eq 0 ]; then
        echo "NOT REFUSED: $overrides"
        fail=1
    elif ! grep -qF "$want" <<< "$out"; then
        echo "REFUSED WITHOUT SAYING WHY: $overrides"
        echo "  wanted the words: $want"
        echo "  said: $(tail -2 <<< "$out" | tr '\n' ' ')"
        fail=1
    fi
done < "$chart/testdata/refusals.txt"
if [ "$fail" -eq 0 ]; then
    echo "every refusal holds"
fi
exit "$fail"
