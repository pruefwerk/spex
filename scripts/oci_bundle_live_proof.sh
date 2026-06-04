#!/bin/sh
set -eu

ROOT=${ROOT:-$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)}
GO_RUN=${GO_RUN:-go run}
REGISTRY_HOST=${REGISTRY_HOST:-127.0.0.1}
REGISTRY_PORT=${REGISTRY_PORT:-5050}
REGISTRY_CONTAINER=${REGISTRY_CONTAINER:-spex-oci-bundle-registry-${REGISTRY_PORT}}
REGISTRY_IMAGE=${REGISTRY_IMAGE:-registry:2}
REPOSITORY=${REPOSITORY:-${REGISTRY_HOST}:${REGISTRY_PORT}/spex-bundles/custom-echo}
TAG=${TAG:-0.1.0-live-proof}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 127
  fi
}

require_command docker
require_command oras
require_command go

cleanup_registry=false
if ! docker ps --format '{{.Names}}' | grep -qx "$REGISTRY_CONTAINER"; then
  if docker ps -a --format '{{.Names}}' | grep -qx "$REGISTRY_CONTAINER"; then
    docker start "$REGISTRY_CONTAINER" >/dev/null
  else
    docker run -d --name "$REGISTRY_CONTAINER" -p "${REGISTRY_PORT}:5000" "$REGISTRY_IMAGE" >/dev/null
    cleanup_registry=true
  fi
fi

ready=false
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if oras repo ls --plain-http "${REGISTRY_HOST}:${REGISTRY_PORT}" >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
if [ "$ready" != "true" ]; then
  echo "OCI registry did not become ready at ${REGISTRY_HOST}:${REGISTRY_PORT}" >&2
  exit 2
fi

TMPDIR=${TMPDIR:-/tmp}
WORKDIR=$(mktemp -d "${TMPDIR%/}/spex-oci-bundle-live-proof.XXXXXX")
cleanup() {
  if [ "${KEEP_OCI_BUNDLE_LIVE_PROOF:-false}" != "true" ]; then
    rm -rf "$WORKDIR"
  fi
  if [ "$cleanup_registry" = "true" ] && [ "${KEEP_OCI_BUNDLE_REGISTRY:-false}" != "true" ]; then
    docker rm -f "$REGISTRY_CONTAINER" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

cd "$ROOT/examples/bundles/custom-echo"
oras push --plain-http "${REPOSITORY}:${TAG}" \
  bundle.yaml:application/vnd.pruefwerk.spex.bundle.manifest.v1+yaml \
  catalogs/custom-echo-steps.yaml:application/vnd.pruefwerk.spex.bundle.catalog.v1+yaml \
  schemas/custom-connection.schema.yaml:application/schema+yaml \
  schemas/custom-echo-input.schema.yaml:application/schema+yaml \
  schemas/custom-echo-result.schema.yaml:application/schema+yaml

DIGEST=$(oras resolve --plain-http "${REPOSITORY}:${TAG}")
SOURCE="oci://${REPOSITORY}@${DIGEST}"
SUITE="$WORKDIR/custom-bundle-oci-live.yaml"
LOCK="$WORKDIR/spex.bundle-lock.yaml"
VENDOR_DIR="$WORKDIR/vendor"

cat >"$SUITE" <<EOF
apiVersion: spex.suite.v0.1
kind: ScenarioSuite
metadata:
  name: custom-bundle-oci-live
spec:
  bundleRefs:
    - name: custom
      version: 0.1.0
      source: ${SOURCE}
  bindingRef: ${ROOT}/examples/bindings/custom-local.yaml
  scenarios:
    - ${ROOT}/examples/providers/custom/custom-echo.yaml
  workspaceDir: ${WORKDIR}/workspace
  failFast: false
EOF

cd "$ROOT"
export SPEX_OCI_BUNDLE_PLAIN_HTTP=true
export SPEX_OCI_BUNDLE_CACHE_DIR="$WORKDIR/oci-cache"

$GO_RUN ./cmd/spex bundle explain --suite "$SUITE" --format json >"$WORKDIR/bundle-explain.json"
$GO_RUN ./cmd/spex bundle lock --suite "$SUITE" --out "$LOCK"
$GO_RUN ./cmd/spex bundle verify --suite "$SUITE" --lock "$LOCK"
$GO_RUN ./cmd/spex bundle vendor --suite "$SUITE" --out "$VENDOR_DIR"

test -f "$VENDOR_DIR/custom-0.1.0/bundle.yaml"
grep -q "$DIGEST" "$LOCK"

echo "OCI bundle live proof passed: $SOURCE"
echo "proof workspace: $WORKDIR"
