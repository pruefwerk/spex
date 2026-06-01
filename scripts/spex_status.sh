#!/usr/bin/env sh
set -eu

SCENARIO="${SCENARIO:-examples/scenarios/mqtt-ingestion-basic.yaml}"
BINDING="${BINDING:-examples/bindings/local-dev.yaml}"
KEYCLOAK_BINDING="${KEYCLOAK_BINDING:-examples/bindings/local-dev-keycloak.yaml}"
INTEGRATION_PROFILE="${INTEGRATION_PROFILE:-examples/integration/local-kind-profile.yaml}"
KEYCLOAK_INTEGRATION_PROFILE="${KEYCLOAK_INTEGRATION_PROFILE:-examples/integration/local-kind-keycloak-profile.yaml}"
LIVE_REPORT="${LIVE_REPORT:-generated/mqtt-ingestion-basic-live/reports/scenario-run-report.yaml}"
KEYCLOAK_LIVE_REPORT="${KEYCLOAK_LIVE_REPORT:-generated/mqtt-ingestion-basic-keycloak-live/reports/scenario-run-report.yaml}"
KUBE_CONTEXT="${KUBE_CONTEXT:-kind-kind}"
SPEX="${SPEX:-}"
REPO_ROOT="${REPO_ROOT:-$(pwd)}"

run_spex() {
  if [ -n "$SPEX" ]; then
    "$SPEX" "$@"
    return $?
  fi
  env GOCACHE="${GOCACHE:-$REPO_ROOT/.cache/go-build}" go run ./cmd/spex "$@"
}

profile_has_placeholders() {
  profile="$1"
  grep -Eq 'registry\.example\.com|example\.com/platform' "$profile"
}

report_passed() {
  report="$1"
  [ -f "$report" ] && grep -Eq '^[[:space:]]+result: passed$' "$report"
}

require_file() {
  if [ ! -f "$1" ]; then
    echo "missing: $1"
    exit 2
  fi
}

require_file "$SCENARIO"
require_file "$BINDING"
require_file "$KEYCLOAK_BINDING"
require_file "$INTEGRATION_PROFILE"
require_file "$KEYCLOAK_INTEGRATION_PROFILE"

echo "production-candidate status"
echo

run_spex validate --scenario "$SCENARIO" --binding "$BINDING" >/dev/null
echo "ok: base scenario and binding validate"

run_spex validate --scenario "$SCENARIO" --binding "$KEYCLOAK_BINDING" >/dev/null
echo "ok: Keycloak binding validates"

run_spex validate --scenario "$SCENARIO" --binding "$BINDING" --integration-profile "$INTEGRATION_PROFILE" --kube-context "$KUBE_CONTEXT" >/dev/null
echo "ok: kind integration profile validates"

run_spex validate --scenario "$SCENARIO" --binding "$KEYCLOAK_BINDING" --integration-profile "$KEYCLOAK_INTEGRATION_PROFILE" --kube-context "$KUBE_CONTEXT" >/dev/null
echo "ok: Keycloak kind integration profile validates"

echo
echo "implemented locally:"
echo "- scenario and binding parser/validator"
echo "- KUTTL workspace generator"
echo "- MQTT publish probe"
echo "- Redpanda snapshot-offsets and contains probes"
echo "- GraphQL expect probe"
echo "- Keycloak client-credentials auth"
echo "- KUTTL-native kind/profile generation"
echo "- runner cleanup, evidence collection, and ScenarioRunReport"

echo
if profile_has_placeholders "$INTEGRATION_PROFILE" || profile_has_placeholders "$KEYCLOAK_INTEGRATION_PROFILE"; then
  echo "live proof: blocked"
  echo "reason: integration profiles still contain placeholder chart/image coordinates"
  echo "next: copy the profile and replace registry.example.com/example.com platform refs with real MQTT, Redpanda, GraphQL, and optional Keycloak installs"
else
  if report_passed "$LIVE_REPORT" && report_passed "$KEYCLOAK_LIVE_REPORT"; then
    echo "live proof: complete"
    echo "ok: base live report passed at $LIVE_REPORT"
    echo "ok: Keycloak live report passed at $KEYCLOAK_LIVE_REPORT"
  else
    echo "live proof: ready to attempt"
    echo "next: run scripts/integration_live.sh with real SPEX_* secrets and host tools available"
  fi
fi
