#!/usr/bin/env bash
#
# Build this repository's images and package its chart EXACTLY the way a
# release does: GoReleaser builds and pushes every image through ko, then
# helmctl reads what it pushed and bakes the digests into the chart — see
# .goreleaser.yaml's own header and release.yaml, which packages the same
# chart the same way for a real tag.
#
# What is different here, and only here:
#
#   - the destination is the kind box's own registry, not ghcr.io, and no
#     GitHub release is cut;
#   - every image is built for ONE architecture, the box's node's own,
#     rather than every architecture a release always carries — a second
#     one would only be built and discarded. That narrowing is produced by
#     hack/goreleaser-snapshot-config from THIS file, never a second
#     release configuration kept beside it (ported from truvity/policy,
#     which built this the same way for its own kind tier).
#
# NEVER --snapshot: GoReleaser's snapshot mode also stops every image
# being pushed, which is exactly the one thing this needs. `--skip=validate`
# is what makes a commit that is not a tag releasable instead.
set -euo pipefail
cd "$(dirname "$0")/.."

SNAPSHOT_REGISTRY=${SNAPSHOT_REGISTRY:-localhost:5001}
IMAGE_TAG=${IMAGE_TAG:-snapshot-$(git rev-parse --short HEAD)}

export KO_DOCKER_REPO="${SNAPSHOT_REGISTRY}/audit"
export IMAGE_TAG
export GITHUB_RELEASE_DISABLED=true

# A buildx builder of THIS script's own, on the HOST's network — see
# truvity/policy's identical hack/example-snapshot.sh for why: a
# docker-container builder in its own network namespace cannot reach
# "localhost", and the plain docker driver refuses the SBOM attestation
# GoReleaser's ko pipe asks for.
BUILDER=audit-e2e-snapshot
if ! docker buildx inspect "$BUILDER" >/dev/null 2>&1; then
    docker buildx create --name "$BUILDER" --driver docker-container --driver-opt network=host >/dev/null
fi
export BUILDX_BUILDER="$BUILDER"

step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

step "the release configuration, for linux/${SNAPSHOT_ARCH:-$(go env GOARCH)}"
go run ./hack/goreleaser-snapshot-config \
    -platform "linux/${SNAPSHOT_ARCH:-$(go env GOARCH)}" \
    -in .goreleaser.yaml \
    -out "$scratch/goreleaser.yaml"

step "waiting for ${SNAPSHOT_REGISTRY} to answer"
registry_deadline=$((SECONDS + 60))
until curl --fail --silent --output /dev/null "http://${SNAPSHOT_REGISTRY}/v2/"; do
    if [ "$SECONDS" -ge "$registry_deadline" ]; then
        echo "the registry at ${SNAPSHOT_REGISTRY} never answered /v2/ within 60s" >&2
        exit 1
    fi
    sleep 1
done

step "the images, built and pushed to ${SNAPSHOT_REGISTRY}"
goreleaser release --config "$scratch/goreleaser.yaml" --clean --skip=validate,announce

step "the chart, packaged from what was just pushed"
# `go run`, not a committed `bin/helmctl` wrapper (truvity/policy keeps
# one): this repository's .gitignore keeps `bin/` out of every commit on
# purpose ("nothing built is committed"), and one call site does not earn
# an exception to that. Version-pinned by go.mod's own
# `tool github.com/truvity/ocictl/cmd/helmctl`.
rm -rf dist/charts
go run github.com/truvity/ocictl/cmd/helmctl goreleaser-manifest --goreleaser-dist dist -o dist/goreleaser-manifest.json
go run github.com/truvity/ocictl/cmd/helmctl package \
    --chart charts/audit \
    --manifest dist/goreleaser-manifest.json \
    --require-image-digests \
    --output dist/charts

ls dist/charts
