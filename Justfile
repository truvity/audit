# Recipes for audit: what CI and people run. Run them inside the devbox shell (`devbox shell`, or `devbox run -- just <recipe>`).

set shell := ["bash", "-euo", "pipefail", "-c"]

default:
    @just --list

# Everything CI runs, in one go: the documentation check, format and lint, build and test, licences and advisories.
check: docs lint test check-licenses check-advisories

# Every YAML file parses, every relative markdown link and its anchor resolves, and the requirement rows are well formed.
# It needs a Python 3 with PyYAML; the CI job installs PyYAML into a virtual environment of its own.
docs:
    python3 .github/scripts/check.py

# Format and lint: Go, Rust, the shell scripts and the workflows.
lint: go-lint rust-lint shell-lint workflow-lint

# Build every library.
build: go-build rust-build

# Test every library.
test: go-test rust-test

# The Go modules of the workspace (go.work), one directory per line.
[private]
go-modules:
    @go work edit -json | jq -r '.Use[].DiskPath'

# Build every Go module.
go-build:
    #!/usr/bin/env bash
    set -euo pipefail
    for d in $(just go-modules); do
        echo "go build: $d"
        (cd "$d" && go build -o /dev/null ./... && go vet ./...)
    done

# Test every Go module with the race detector, uncached. The lines of passing tests are dropped, and the exit status is that of
# go test.
go-test:
    #!/usr/bin/env bash
    set -euo pipefail
    for d in $(just go-modules); do
        echo "go test: $d"
        (cd "$d" && go test -race -count=1 -v ./... 2>&1 \
          | awk '!/^=== (RUN|PAUSE|CONT|NAME)/ && !/^[[:space:]]*--- PASS/ && !/^PASS$/ && !/^no test files$/' ; test "${PIPESTATUS[0]}" -eq 0)
    done

# Lint every Go module with the one configuration of the root.
go-lint:
    #!/usr/bin/env bash
    set -euo pipefail
    root=$PWD
    for d in $(just go-modules); do
        echo "golangci-lint: $d"
        (cd "$d" && golangci-lint run --config "$root/.golangci.yaml" ./...)
    done

rust-build: rust-toolchain
    cargo build --workspace --locked

rust-test: rust-toolchain
    cargo test --workspace --locked

rust-lint: rust-toolchain
    cargo fmt --all --check
    cargo clippy --workspace --all-targets --locked -- -D warnings
    RUSTDOCFLAGS="-D warnings" cargo doc --workspace --no-deps --locked

# The licence rule for Rust: `cargo deny check licenses` over Cargo.lock, with the allow list of deny.toml. No network.
check-licenses: rust-toolchain
    cargo deny --locked check licenses

# The advisory gate: govulncheck over every Go module and cargo audit over Cargo.lock. Needs the network (vuln.go.dev, the
# RustSec database).
check-advisories: rust-toolchain
    #!/usr/bin/env bash
    set -euo pipefail
    for d in $(just go-modules); do
        echo "govulncheck: $d"
        (cd "$d" && govulncheck ./...)
    done
    cargo audit --deny warnings

# shellcheck over every tracked shell script.
shell-lint:
    git ls-files -z -- '*.sh' | xargs -0 --no-run-if-empty shellcheck -x

# actionlint over the workflows.
workflow-lint:
    actionlint

# Install and check the pinned Rust toolchain (`devbox run -- just rust-toolchain`). It is safe to run at any time and in parallel: a
# complete toolchain is a check of a fraction of a second; an incomplete one is installed under a lock, by one process at a time. The
# recipes that run cargo depend on it.
[private]
rust-toolchain:
    #!/usr/bin/env bash
    set -euo pipefail
    home=${RUSTUP_HOME:?RUSTUP_HOME is not set (run it as: devbox run -- just rust-toolchain)}
    channel=$(sed -n 's/^channel *= *"\(.*\)"/\1/p' rust-toolchain.toml)
    [ -n "$channel" ] || { echo "::error::rust-toolchain.toml names no channel" >&2; exit 1; }
    # The emitter is a library: the toolchain names no extra target.
    targets=$(grep -o '[a-z0-9_]*-[a-z0-9_]*-[a-z0-9_-]*' <<<"$(sed -n 's/^targets *= *//p' rust-toolchain.toml)" || true)
    export RUSTUP_AUTO_INSTALL=0
    # What a complete toolchain lacks, on one line; nothing when it is complete.
    missing() {
        local m="" installed t
        cargo --version >/dev/null 2>&1 || m="$m cargo"
        rustc --version >/dev/null 2>&1 || m="$m rustc"
        cargo clippy --version >/dev/null 2>&1 || m="$m clippy"
        cargo fmt --version >/dev/null 2>&1 || m="$m rustfmt"
        installed=$(rustup target list --installed 2>/dev/null || true)
        for t in $targets; do grep -qx "$t" <<<"$installed" || m="$m target:$t"; done
        echo "${m# }"
    }
    done_line() { echo "rust-toolchain: $(rustc --version)"; }
    if [ -z "$(missing)" ]; then done_line; exit 0; fi
    # One installer at a time (mkdir is atomic and portable); a lock whose process is gone is stale.
    mkdir -p "$home"
    lock="$home/.install.lock"
    waited=0
    until mkdir "$lock" 2>/dev/null; do
        holder=$(cat "$lock/pid" 2>/dev/null || true)
        if [ -n "$holder" ] && ! kill -0 "$holder" 2>/dev/null; then rm -rf "$lock"; continue; fi
        [ "$waited" -lt 600 ] || { echo "::error::rust-toolchain: $lock has been held by pid ${holder:-?} for ten minutes; if no rust-toolchain runs, remove it" >&2; exit 1; }
        [ "$waited" -ne 0 ] || echo "rust-toolchain: waiting for the install that pid ${holder:-?} runs" >&2
        sleep 1
        waited=$((waited + 1))
    done
    echo $$ >"$lock/pid"
    trap 'rm -rf "$lock"' EXIT
    # The install we waited for may have finished the job.
    if [ -z "$(missing)" ]; then done_line; exit 0; fi
    for attempt in 1 2 3; do
        if ls -d "$home/toolchains/$channel"-* >/dev/null 2>&1; then
            echo "::warning::rust-toolchain: the toolchain $channel in $home is not complete (missing: $(missing)); a killed or interrupted install leaves it so. Removing it and installing again." >&2
            rm -rf "$home/toolchains/$channel"-* "$home/downloads" "$home/tmp" "$home/update-hashes"
        fi
        if rustup toolchain install && [ -z "$(missing)" ]; then
            done_line
            exit 0
        fi
        echo "::warning::rust-toolchain: attempt $attempt failed; the rustup, cargo and rustc processes now:" >&2
        ps -eo pid,etimes,args | grep -E '[r]ustup|[c]argo|[r]ustc' >&2 || echo "(none)" >&2
        sleep $((attempt * 3))
    done
    echo "::error::rust-toolchain: the toolchain $channel could not be installed in three attempts (still missing: $(missing)); remove $home and run it again" >&2
    exit 1
