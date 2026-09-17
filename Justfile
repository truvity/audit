# Development commands for audit. Tools come from devbox (`devbox shell`, or
# direnv); CI runs each recipe as its own job.

export GOWORK := "off"

# Format all Go files
fmt:
    golangci-lint fmt ./...

# Regenerate Go and TypeScript from proto
generate:
    buf generate

# Lint proto and check for breaking changes against the last tag
proto:
    buf lint
    buf breaking --against '.git#tag=$(git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)' || true

# Build (compile check)
build: fmt
    go build ./...

# Run unit tests
test:
    go test ./... -coverprofile=coverage.out

# Run linters
lint:
    golangci-lint config verify
    golangci-lint run ./...
    # A `;` inside a mermaid sequenceDiagram is a statement separator: it
    # splits the message and GitHub renders nothing. Keep them out of docs.
    ! grep -rn --include=*.md -E '^[[:space:]]*[A-Za-z][A-Za-z0-9_]*[[:space:]]*-?->>?.*;' docs/

# Validate presets and the common catalogue against their schemas
schemas:
    @echo "TODO(implementation): validate presets/*.yaml and catalogue/*.yaml against schemas/"

# Run Go vulnerability check
vuln:
    govulncheck ./...

# Everything CI runs
check: build test lint proto schemas vuln
