# Reference Scenario Repository

This directory models the separate repository a tester team would own.

It intentionally references shared platform assets from `../bindings`, `../integration`, and `../catalogs` to demonstrate how a scenario repo can stay small while target bindings and integration profiles are owned elsewhere.

## Non-Cluster Gate

```sh
make -C examples/reference-scenario-repo ci SPEX="../../spex"
```

When using the source tree directly:

```sh
make -C examples/reference-scenario-repo ci SPEX="env GOCACHE=.cache/go-build go run ../../cmd/spex"
```

## Production Gate

```sh
SPEX_PRODUCTION_CHECK=true make -C examples/reference-scenario-repo ci
```

## Live Proof

```sh
make -C examples/reference-scenario-repo live \
  PROBE_IMAGE=spex-probe:dev \
  SPEX_MQTT_USERNAME=tester \
  SPEX_MQTT_PASSWORD=tester-password \
  SPEX_GRAPHQL_KEYCLOAK_CLIENT_SECRET=graphql-client-secret \
  SPEX_KEYCLOAK_ADMIN_PASSWORD=admin-password
```
