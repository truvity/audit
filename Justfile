# Development commands for audit. Tools come from devbox (`devbox shell`, or
# direnv); CI runs each recipe as its own job.

export GOWORK := "off"

# Format all Go files
fmt:
    golangci-lint fmt ./...

# Regenerate Go, TypeScript and the published JSON Schema from the proto
generate:
    buf generate
    go run ./cmd/audit schema

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

# Run the tests under the race detector. The emitter hands records to a
# background writer, so a data race here would be a lost or duplicated record
# rather than a crash, and would not show up in ordinary runs.
race:
    CGO_ENABLED=1 go test -race ./...

# Run linters
lint:
    golangci-lint config verify
    golangci-lint run ./...
    # A `;` inside a mermaid sequenceDiagram is a statement separator: it
    # splits the message and GitHub renders nothing. Keep them out of docs.
    ! grep -rn --include=*.md -E '^[[:space:]]*[A-Za-z][A-Za-z0-9_]*[[:space:]]*-?->>?.*;' docs/

# Hold this repository's own presets and catalogue to the contracts it publishes
schemas:
    go run ./cmd/audit validate --presets presets catalogue/common.yaml

# Run Go vulnerability check
vuln:
    govulncheck ./...

# Everything CI runs
check: build test race lint proto schemas vuln
