#!/usr/bin/env bash
# Assemble the site that GitHub Pages serves: every schema this repository
# names by an $id, at the path its $id says. A file whose $id does not match
# the path it would be served at fails here, because an identifier that does
# not resolve is the fault this site exists to remove.
set -euo pipefail
cd "$(dirname "$0")/.."

base="https://truvity.github.io/audit/schemas/v1"
out="${1:-_site}"
rm -rf "$out"
mkdir -p "$out/schemas/v1/common" "$out/schemas/v1/config"

publish() { # <source file> <path under schemas/v1>
  local id
  id=$(jq -r '."$id"' "$1")
  if [ "$id" != "$base/$2" ]; then
    echo "pages-site: $1 has \$id $id, want $base/$2" >&2
    exit 1
  fi
  cp "$1" "$out/schemas/v1/$2"
}

publish gen/jsonschema/record.v1.schema.json record.schema.json
for f in catalogue extension preset; do
  publish "schemas/$f.schema.json" "$f.schema.json"
done
for f in schemas/config/*.schema.json; do
  publish "$f" "config/$(basename "$f")"
done
for f in catalogue/*.json; do
  publish "$f" "common/$(basename "$f")"
done

# The schemas are served as they are; a browser landing on the site gets a
# list rather than a 404.
{
  echo '<!doctype html><meta charset="utf-8"><title>audit schemas</title><h1>audit schemas</h1><ul>'
  (cd "$out/schemas/v1" && find . -name '*.json' | sort | sed 's#^\./##' \
    | sed 's#.*#<li><a href="schemas/v1/&">&</a></li>#')
  echo '</ul>'
} > "$out/index.html"
echo "pages-site: $(find "$out/schemas" -name '*.json' | wc -l) schemas in $out"
