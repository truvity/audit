#!/usr/bin/env python3
"""The check of this repository: every YAML file parses, every relative markdown link resolves,
and the requirement rows are well formed.

Run it from anywhere inside the repository:  python3 .github/scripts/check.py

Links to other sites (http, https, mailto) are not fetched: a check that depends on the network
fails for reasons that are not in the pull request. Relative links, and the heading anchors they name,
are checked."""

import os
import re
import subprocess
import sys

import yaml

ROOT = subprocess.run(
    ["git", "rev-parse", "--show-toplevel"], check=True, capture_output=True, text=True
).stdout.strip()

errors = []


def fail(path, msg):
    errors.append(f"{os.path.relpath(path, ROOT)}: {msg}")


def tracked(*suffixes):
    out = subprocess.run(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
        check=True, capture_output=True, text=True, cwd=ROOT,
    ).stdout
    for name in out.split("\0"):
        if name and name.endswith(suffixes) and os.path.exists(os.path.join(ROOT, name)):
            yield os.path.join(ROOT, name)


# 1. YAML parses.
yaml_docs = {}
for path in tracked(".yaml", ".yml"):
    try:
        with open(path, encoding="utf-8") as f:
            yaml_docs[path] = list(yaml.safe_load_all(f))
    except yaml.YAMLError as e:
        fail(path, f"YAML does not parse: {str(e).splitlines()[0] if str(e) else e}")

# 2. Markdown links.
FENCE = re.compile(r"^\s*(```|~~~)")
INLINE_CODE = re.compile(r"`[^`\n]*`")
LINK = re.compile(r"(?<!\!)\[(?:[^\[\]]|\[[^\]]*\])*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)")
HEADING = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")


def slug(text):
    text = INLINE_CODE.sub(lambda m: m.group(0).strip("`"), text)
    text = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", text)
    text = text.strip().lower()
    text = re.sub(r"[^\w\- ]", "", text, flags=re.UNICODE)
    return text.replace(" ", "-")


def body_lines(path):
    """The lines of a markdown file outside fenced code blocks."""
    in_fence = False
    with open(path, encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            if FENCE.match(line):
                in_fence = not in_fence
                continue
            if not in_fence:
                yield n, line.rstrip("\n")


anchors_cache = {}


def anchors(path):
    if path not in anchors_cache:
        seen, found = {}, set()
        for _, line in body_lines(path):
            m = HEADING.match(line)
            if m:
                s = slug(m.group(2))
                k = seen.get(s, 0)
                seen[s] = k + 1
                found.add(s if k == 0 else f"{s}-{k}")
        anchors_cache[path] = found
    return anchors_cache[path]


for path in tracked(".md"):
    for n, line in body_lines(path):
        for m in LINK.finditer(INLINE_CODE.sub("", line)):
            target = m.group(1)
            if re.match(r"^[a-z][a-z0-9+.-]*:", target, re.I) or target.startswith("//"):
                continue
            file_part, _, frag = target.partition("#")
            dest = path if file_part == "" else os.path.normpath(os.path.join(os.path.dirname(path), file_part))
            if file_part and not os.path.exists(dest):
                fail(path, f"line {n}: the link target does not exist: {target}")
                continue
            if frag and os.path.isfile(dest) and dest.endswith(".md"):
                if frag.lower() not in anchors(dest):
                    fail(path, f"line {n}: no heading for the anchor: {target}")

# 3. Requirement rows.
KEYWORDS = re.compile(r"\b(MUST NOT|MUST|SHOULD NOT|SHOULD|MAY)\b")
for path, docs in yaml_docs.items():
    if os.path.relpath(path, ROOT) != "docs/requirements/audit.yaml":
        continue
    rows = docs[0].get("requirements", []) if docs and isinstance(docs[0], dict) else []
    ids = [r.get("id") for r in rows]
    for dup in {i for i in ids if ids.count(i) > 1}:
        fail(path, f"duplicate id {dup}")
    known = set(ids)
    for r in rows:
        rid = r.get("id")
        if not re.fullmatch(r"REQ-AUDIT-\d{3}", str(rid)):
            fail(path, f"bad id {rid}")
        if r.get("status") == "withdrawn":
            continue
        kws = KEYWORDS.findall(r.get("statement", ""))
        if len(kws) != 1:
            fail(path, f"{rid}: the statement has {len(kws)} keywords, not one")
        elif kws[0].split()[0] != r.get("level"):
            fail(path, f"{rid}: the keyword {kws[0]} does not match level {r.get('level')}")
        if r.get("owner") == "audit" and r.get("level") == "MUST" and not r.get("verification"):
            fail(path, f"{rid}: a MUST needs a verification")
        if r.get("split_from") and r["split_from"] not in known:
            fail(path, f"{rid}: split_from names an unknown id {r['split_from']}")
        if r.get("owner") == "sluis" and not r.get("link"):
            fail(path, f"{rid}: a row that stays in sluis needs a link")

if errors:
    print("\n".join(errors))
    print(f"\n{len(errors)} problem(s).")
    sys.exit(1)
print("ok: YAML parses, relative links and anchors resolve, requirement rows are well formed.")
