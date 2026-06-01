#!/usr/bin/env sh
set -eu

INTEGRATION_CONFIG="${INTEGRATION_CONFIG:-}"

set_default_config_value() {
  key="$1"
  value="$2"
  export "$key=$value"
}

is_config_value_set() {
  case "$1" in
    SCENARIO) [ "${SCENARIO+x}" ] ;;
    BINDING) [ "${BINDING+x}" ] ;;
    WORKSPACE) [ "${WORKSPACE+x}" ] ;;
    RUN_ID) [ "${RUN_ID+x}" ] ;;
    KUBECTL) [ "${KUBECTL+x}" ] ;;
    SPEX) [ "${SPEX+x}" ] ;;
    NAMESPACE) [ "${NAMESPACE+x}" ] ;;
    KUBE_CONTEXT) [ "${KUBE_CONTEXT+x}" ] ;;
    INTEGRATION_PROFILE) [ "${INTEGRATION_PROFILE+x}" ] ;;
    PROBE_IMAGE) [ "${PROBE_IMAGE+x}" ] ;;
    PROBE_IMAGE_PULL_POLICY) [ "${PROBE_IMAGE_PULL_POLICY+x}" ] ;;
    START_KIND_IN_KUTTL) [ "${START_KIND_IN_KUTTL+x}" ] ;;
    INTEGRATION_ALLOW_PLACEHOLDER_PROFILE) [ "${INTEGRATION_ALLOW_PLACEHOLDER_PROFILE+x}" ] ;;
    INTEGRATION_RUN_KUTTL) [ "${INTEGRATION_RUN_KUTTL+x}" ] ;;
    REPO_ROOT) [ "${REPO_ROOT+x}" ] ;;
    SPEX_[A-Z0-9_]*) eval '[ "${'"$1"'+x}" ]' ;;
    *) return 1 ;;
  esac
}

load_config_defaults() {
  if [ -z "$INTEGRATION_CONFIG" ]; then
    return 0
  fi
  if [ ! -f "$INTEGRATION_CONFIG" ]; then
    echo "INTEGRATION_CONFIG does not exist: $INTEGRATION_CONFIG" >&2
    exit 2
  fi
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      ""|\#*) continue ;;
    esac
    key="${line%%=*}"
    value="${line#*=}"
    case "$key" in
      SCENARIO|BINDING|WORKSPACE|RUN_ID|KUBECTL|SPEX|NAMESPACE|KUBE_CONTEXT|INTEGRATION_PROFILE|PROBE_IMAGE|PROBE_IMAGE_PULL_POLICY|START_KIND_IN_KUTTL|INTEGRATION_ALLOW_PLACEHOLDER_PROFILE|INTEGRATION_RUN_KUTTL|REPO_ROOT|SPEX_[A-Z0-9_]*) ;;
      *)
        echo "unsupported INTEGRATION_CONFIG key: $key" >&2
        exit 2
        ;;
    esac
    if ! is_config_value_set "$key"; then
      set_default_config_value "$key" "$value"
    fi
  done < "$INTEGRATION_CONFIG"
}

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 2
  fi
}

bool() {
  case "$2" in
    true|false) ;;
    *)
      echo "$1 must be true or false; got: $2" >&2
      exit 2
      ;;
  esac
}

reject_placeholder_profile() {
  if [ -z "$INTEGRATION_PROFILE" ] || [ "$INTEGRATION_ALLOW_PLACEHOLDER_PROFILE" = "true" ]; then
    return 0
  fi
  if grep -Eq 'registry\.example\.com|example\.com/platform' "$INTEGRATION_PROFILE"; then
    echo "INTEGRATION_PROFILE contains placeholder chart/image coordinates: $INTEGRATION_PROFILE" >&2
    echo "copy the profile and replace placeholders with real Helm charts, manifests, or images" >&2
    exit 2
  fi
}

check_profile_env_var() {
  name="$1"
  eval "value=\${$name:-}"
  if [ -z "$value" ]; then
    echo "INTEGRATION_PROFILE references \$$name but $name is not set" >&2
    exit 2
  fi
}

check_required_profile_env() {
  if [ "$INTEGRATION_RUN_KUTTL" != "true" ] || [ -z "$INTEGRATION_PROFILE" ]; then
    return 0
  fi
  for name in $(grep -Eo '\$SPEX_[A-Z0-9_]+' "$INTEGRATION_PROFILE" | sed 's/^\$//' | sort -u); do
    check_profile_env_var "$name"
  done
}

profile_references_command() {
  command_name="$1"
  grep -Eq "(^|[[:space:];|])$command_name([[:space:];|]|$)" "$INTEGRATION_PROFILE"
}

check_required_profile_tools() {
  if [ "$INTEGRATION_RUN_KUTTL" != "true" ] || [ -z "$INTEGRATION_PROFILE" ]; then
    return 0
  fi
  for command_name in docker helm kind kubectl; do
    if profile_references_command "$command_name"; then
      need "$command_name"
    fi
  done
}

run_spex() {
  if [ -n "$SPEX" ]; then
    "$SPEX" "$@"
    return $?
  fi
  env GOCACHE="${GOCACHE:-$REPO_ROOT/.cache/go-build}" go run ./cmd/spex "$@"
}

load_config_defaults

SCENARIO="${SCENARIO:-examples/scenarios/mqtt-ingestion-basic.yaml}"
BINDING="${BINDING:-examples/bindings/local-dev.yaml}"
WORKSPACE="${WORKSPACE:-generated/mqtt-ingestion-basic-integration}"
RUN_ID="${RUN_ID:-run-integration}"
KUBECTL="${KUBECTL:-kubectl}"
SPEX="${SPEX:-}"
NAMESPACE="${NAMESPACE:-}"
KUBE_CONTEXT="${KUBE_CONTEXT:-}"
INTEGRATION_PROFILE="${INTEGRATION_PROFILE:-}"
PROBE_IMAGE="${PROBE_IMAGE:-}"
PROBE_IMAGE_PULL_POLICY="${PROBE_IMAGE_PULL_POLICY:-}"
START_KIND_IN_KUTTL="${START_KIND_IN_KUTTL:-false}"
INTEGRATION_ALLOW_PLACEHOLDER_PROFILE="${INTEGRATION_ALLOW_PLACEHOLDER_PROFILE:-false}"
INTEGRATION_RUN_KUTTL="${INTEGRATION_RUN_KUTTL:-true}"
REPO_ROOT="${REPO_ROOT:-$(pwd)}"

if [ -z "${SPEX:-}" ]; then
  need go
fi
bool START_KIND_IN_KUTTL "$START_KIND_IN_KUTTL"
bool INTEGRATION_ALLOW_PLACEHOLDER_PROFILE "$INTEGRATION_ALLOW_PLACEHOLDER_PROFILE"
bool INTEGRATION_RUN_KUTTL "$INTEGRATION_RUN_KUTTL"
if [ "$INTEGRATION_RUN_KUTTL" = "true" ]; then
  need "$KUBECTL"
fi

if [ -n "$INTEGRATION_PROFILE" ] && [ ! -f "$INTEGRATION_PROFILE" ]; then
  echo "INTEGRATION_PROFILE does not exist: $INTEGRATION_PROFILE" >&2
  exit 2
fi
reject_placeholder_profile
check_required_profile_env
check_required_profile_tools

set -- validate \
  --scenario "$SCENARIO" \
  --binding "$BINDING"
if [ -n "$INTEGRATION_PROFILE" ]; then
  set -- "$@" --integration-profile "$INTEGRATION_PROFILE"
fi
if [ -n "$KUBE_CONTEXT" ]; then
  set -- "$@" --kube-context "$KUBE_CONTEXT"
fi
if [ -n "$NAMESPACE" ]; then
  set -- "$@" --namespace "$NAMESPACE"
fi
run_spex "$@"

set -- compile \
  --scenario "$SCENARIO" \
  --binding "$BINDING" \
  --out "$WORKSPACE" \
  --run-id "$RUN_ID"
if [ -n "$INTEGRATION_PROFILE" ]; then
  set -- "$@" --integration-profile "$INTEGRATION_PROFILE"
fi
if [ -n "$KUBE_CONTEXT" ]; then
  set -- "$@" --kube-context "$KUBE_CONTEXT"
fi
if [ -n "$NAMESPACE" ]; then
  set -- "$@" --namespace "$NAMESPACE"
fi
if [ -n "$PROBE_IMAGE" ]; then
  set -- "$@" --probe-image "$PROBE_IMAGE"
fi
if [ -n "$PROBE_IMAGE_PULL_POLICY" ]; then
  set -- "$@" --probe-image-pull-policy "$PROBE_IMAGE_PULL_POLICY"
fi
if [ -n "$REPO_ROOT" ]; then
  set -- "$@" --repo-root "$REPO_ROOT"
fi
if [ "$START_KIND_IN_KUTTL" = "true" ]; then
  set -- "$@" --start-kind
fi

run_spex "$@"
if [ "$INTEGRATION_RUN_KUTTL" = "true" ]; then
  run_spex run --workspace "$WORKSPACE" --command "$KUBECTL"
else
  echo "workspace compiled: $WORKSPACE"
fi
