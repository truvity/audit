# Development commands for audit. Tools come from devbox (`devbox shell`, or
# direnv); CI runs each recipe as its own job.

export GOWORK := "off"

# Format all Go files
fmt:
    golangci-lint fmt ./...

# Regenerate everything from the proto: Go, the published JSON Schema, and
# TypeScript. The TypeScript plugin is fetched from the schema registry, so this
# needs the network.
generate:
    buf generate

# Regenerate only what local plugins produce: Go, and the record's JSON Schema.
generate-local:
    buf generate --template buf.gen.local.yaml

# Generated code is committed. A change to the proto that is not followed by
# `just generate` leaves the tree dirty here.
#
# The gate checks only what local plugins produce, so that it needs nothing but
# this checkout: the TypeScript plugin comes from a remote registry that rate
# limits, and a gate that fails because somebody else was generating code is a
# gate people learn to ignore. `drift-ts` is the same check for TypeScript and
# belongs in CI, where a retry is cheap.
drift: generate-local
    git diff --exit-code -- gen

drift-ts: generate
    git diff --exit-code -- gen ts/src/gen

# Lint proto and check that nothing released has changed incompatibly
proto:
    buf lint
    # A first release has nothing to be incompatible with, and saying so beats
    # a recipe that always passes because its comparison silently failed.
    if tag=$(git describe --tags --abbrev=0 2>/dev/null); then \
        buf breaking --against ".git#tag=$tag"; \
    else \
        echo "no release tag yet: nothing to compare against"; \
    fi

# Build (compile check)
build: fmt
    go build ./...

# Run unit tests
test:
    go test ./... -coverprofile=coverage.out

# Run the whole suite against a real Postgres.
#
# The index is the one part of this repository that cannot be tested without a
# database, and mocking it would prove nothing about the idempotency that is its
# whole contract. So those tests skip when AUDIT_POSTGRES_URL is unset: a
# contributor without Postgres still runs everything else, and `check` stays
# hermetic. This runs the whole suite rather than `./index/...`, because the
# writer's end-to-end tests need the database too.
# CI runs this as its own job with a service container.
#
# This starts a Postgres under .devbox, initialising it on first use.
test-postgres:
    #!/usr/bin/env bash
    set -euo pipefail
    export PGDATA="$PWD/.devbox/virtenv/postgresql/data"
    export PGHOST="$PWD/.devbox/virtenv/postgresql"
    mkdir -p "$PGHOST"
    [ -d "$PGDATA/base" ] || initdb -U postgres --auth=trust >/dev/null
    pg_ctl status -D "$PGDATA" >/dev/null 2>&1 || \
        pg_ctl -D "$PGDATA" -o "-k $PGHOST -c listen_addresses=" -l "$PGHOST/log" start -w
    createdb -h "$PGHOST" -U postgres audit_test 2>/dev/null || true
    AUDIT_POSTGRES_URL="postgres://postgres@/audit_test?host=$PGHOST" go test ./...

# Stop the Postgres that `test-postgres` started
stop-postgres:
    pg_ctl stop -D "$PWD/.devbox/virtenv/postgresql/data" || true

# Run the tests under the race detector. The emitter hands records to a
# background writer, so a data race there would be a lost or duplicated record
# rather than a crash, and would not show up in an ordinary run.
#
# This is not part of `check`, and deliberately. Everything else in this
# repository builds with cgo off, which is what makes the binaries static and
# the images small; the race detector is the one thing that needs a C
# toolchain. Putting it in the gate would mean every contributor needs one to
# run the gate at all. CI runs this as its own job, where the toolchain is the
# runner's own.
race:
    CGO_ENABLED=1 go test -race ./...

# Run linters
lint:
    golangci-lint config verify
    golangci-lint run ./...
    # A `;` inside a mermaid sequenceDiagram is a statement separator: it
    # splits the message and GitHub renders nothing. Keep them out of docs.
    ! grep -rn --include=*.md -E '^[[:space:]]*[A-Za-z][A-Za-z0-9_]*[[:space:]]*-?->>?.*;' docs/

# Hold this repository's own presets and catalogue to the contracts it publishes,
# and hold this repository's own code to its catalogue the way an adopter's is
# held. The second line lists every action the common catalogue declares that
# nothing here emits yet; each is a job for a later milestone, and the digest
# jobs' events sat on that list unnoticed until the tool was pointed at home.
schemas:
    go run ./cmd/audit validate --presets presets catalogue/common.yaml
    go run ./cmd/audit check-emitters . --catalogue catalogue/common.yaml

# Run Go vulnerability check
vuln:
    govulncheck ./...

# Everything CI runs
check: build test lint proto drift schemas vuln
