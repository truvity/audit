#!/usr/bin/env python3
"""Every hook that runs a Pod must bring its own ServiceAccount.

A `pre-install` hook runs BEFORE the release's ordinary resources, so a hook
Pod that names a ServiceAccount the chart creates normally is admitted and
then never gets a Pod: the Job reports `serviceaccount "x" not found` and the
release waits for a hook that can never run. It is not a render error, so
nothing but an install finds it -- which is why it is asserted here.

Reads rendered manifests on stdin. Exits non-zero naming the hook and the
account it would have waited for.
"""

import sys

import yaml

HOOK = "helm.sh/hook"
WEIGHT = "helm.sh/hook-weight"

POD_PATHS = (
    ("spec", "template", "spec"),
    ("spec", "jobTemplate", "spec", "template", "spec"),
)


def dig(doc, path):
    for key in path:
        if not isinstance(doc, dict):
            return None
        doc = doc.get(key)
    return doc if isinstance(doc, dict) else None


def weight(doc):
    return int((doc.get("metadata", {}).get("annotations") or {}).get(WEIGHT, "0"))


def main():
    docs = [d for d in yaml.safe_load_all(sys.stdin) if d]
    accounts = {
        d["metadata"]["name"]: d
        for d in docs
        if d.get("kind") == "ServiceAccount"
    }

    problems = []
    for doc in docs:
        annotations = doc.get("metadata", {}).get("annotations") or {}
        if HOOK not in annotations:
            continue
        for path in POD_PATHS:
            pod = dig(doc, path)
            if pod is None:
                continue
            name = pod.get("serviceAccountName")
            if not name:
                continue
            where = f"{doc['kind']}/{doc['metadata']['name']}"
            account = accounts.get(name)
            if account is None:
                problems.append(f"{where} runs as {name}, which this chart does not create")
            elif HOOK not in (account.get("metadata", {}).get("annotations") or {}):
                problems.append(
                    f"{where} is a hook but runs as {name}, which is an ordinary "
                    f"resource: the hook runs first and the account will not exist yet"
                )
            elif weight(account) >= weight(doc):
                problems.append(
                    f"{where} (weight {weight(doc)}) runs as {name} at weight "
                    f"{weight(account)}: the account must be applied first"
                )

    for problem in problems:
        print(f"HOOK ORDER: {problem}")
    if problems:
        return 1
    print("every hook brings its own service account")
    return 0


if __name__ == "__main__":
    sys.exit(main())
