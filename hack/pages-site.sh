#!/usr/bin/env bash
# Assemble the site that GitHub Pages serves: every schema this repository
# names by an $id, at the path its $id says. A file whose $id does not match
# the path it would be served at fails here, because an identifier that does
# not resolve is the fault this site exists to remove.
set -euo pipefail
cd "$(dirname "$0")/.."

site="https://truvity.github.io/audit/schemas"
out="${1:-_site}"
rm -rf "$out"
mkdir -p "$out/schemas/v1/common" "$out/schemas/v1/config" "$out/schemas/v2/config"

publish() { # <source file> <path under schemas/>, as the $id names it
  local id
  id=$(jq -r '."$id"' "$1")
  if [ "$id" != "$site/$2" ]; then
    echo "pages-site: $1 has \$id $id, want $site/$2" >&2
    exit 1
  fi
  mkdir -p "$out/schemas/$(dirname "$2")"
  cp "$1" "$out/schemas/$2"
}

publish gen/jsonschema/record.v1.schema.json v1/record.schema.json
for f in catalogue extension preset; do
  publish "sdk/schemas/$f.schema.json" "v1/$f.schema.json"
done
# The configuration schemas are versioned by the shape of the document: v2 is
# current, and v1 is kept, frozen, for the one minor in which it is still read.
for f in schemas/config/*.schema.json; do
  publish "$f" "v2/config/$(basename "$f")"
done
for f in schemas/config/v1/*.schema.json; do
  publish "$f" "v1/config/$(basename "$f")"
done
for f in sdk/catalogue/*.json; do
  publish "$f" "v1/common/$(basename "$f")"
done

# The schemas are served as they are; a browser landing on the site gets a
# list rather than a 404.
{
  echo '<!doctype html><meta charset="utf-8"><title>audit schemas</title><h1>audit schemas</h1><ul>'
  (cd "$out/schemas" && find . -name '*.json' | sort | sed 's#^\./##' \
    | sed 's#.*#<li><a href="schemas/&">&</a></li>#')
  echo '</ul>'
} > "$out/index.html"
echo "pages-site: $(find "$out/schemas" -name '*.json' | wc -l) schemas in $out"
