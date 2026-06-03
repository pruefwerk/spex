.PHONY: test dependency-check install-vulncheck vulncheck security-check verify build install release release-check release-archive release-archive-check production-check production-candidate-check reference-scenario-check live-proof-kind live-proof-keycloak probe-image redis-probe-image spex-status compile-example compile-example-keycloak compile-example-kind compile-example-kind-keycloak compile-suite-example validate-suite-example run-example-noop clean-example smoke-probes smoke-integration-script integration-example integration-example-keycloak integration-example-kind-keycloak

GO_RUN ?= env GOCACHE=$(CURDIR)/.cache/go-build go run
GO_BUILD ?= env GOCACHE=$(CURDIR)/.cache/go-build go build
GO_TOOL ?= env GOCACHE=$(CURDIR)/.cache/go-build go
SUITE ?= examples/suites/mqtt-local.yaml
BINDIR ?= $(CURDIR)/.bin
GOVULNCHECK ?= $(BINDIR)/govulncheck
GOVULNCHECK_VERSION ?= v1.1.4
DISTDIR ?= dist
RELEASE_CHECK_DISTDIR ?= $(CURDIR)/.release-check
SMOKE_DIR ?= $(CURDIR)/.tmp/smoke
PRODUCTION_ARTIFACTS ?= reports generated
REQUIRE_PINNED_IMAGES ?= false
PREFIX ?= /usr/local
DESTDIR ?=
VERSION ?= 0.1.0-dev
COMMIT ?= unknown
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
HOST_GOOS ?= $(shell go env GOOS)
HOST_GOARCH ?= $(shell go env GOARCH)
ARCHIVE_NAME ?= spex_$(VERSION)_$(HOST_GOOS)_$(HOST_GOARCH)
RELEASE_ARCHIVE_FLAGS ?=
LDFLAGS := -X 'github.com/pruefwerk/spex/internal/spex.Version=$(VERSION)' -X 'github.com/pruefwerk/spex/internal/spex.BuildCommit=$(COMMIT)' -X 'github.com/pruefwerk/spex/internal/spex.BuildDate=$(BUILD_DATE)'

test:
	$(GO_TOOL) test ./...

dependency-check:
	$(GO_TOOL) mod verify

install-vulncheck:
	mkdir -p $(BINDIR)
	env GOBIN=$(BINDIR) GOCACHE=$(CURDIR)/.cache/go-build go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

vulncheck:
	@if [ ! -x "$(GOVULNCHECK)" ] && ! command -v "$(GOVULNCHECK)" >/dev/null 2>&1; then \
		echo "missing govulncheck at $(GOVULNCHECK); install with: make install-vulncheck" >&2; \
		exit 127; \
	fi
	env GOCACHE=$(CURDIR)/.cache/go-build $(GOVULNCHECK) ./...

security-check: dependency-check vulncheck

verify: dependency-check test smoke-integration-script release-archive-check

build:
	mkdir -p $(BINDIR)
	$(GO_BUILD) -ldflags "$(LDFLAGS)" -o $(BINDIR)/spex ./cmd/spex
	$(GO_BUILD) -ldflags "$(LDFLAGS)" -o $(BINDIR)/spex-probe ./cmd/spex-probe
	$(GO_BUILD) -ldflags "$(LDFLAGS)" -o $(BINDIR)/spex-probe-redis ./cmd/spex-probe-redis
	$(GO_BUILD) -ldflags "$(LDFLAGS)" -o $(BINDIR)/spex-demo-stack ./cmd/spex-demo-stack

install: build
	mkdir -p $(DESTDIR)$(PREFIX)/bin
	install -m 0755 $(BINDIR)/spex $(DESTDIR)$(PREFIX)/bin/spex
	install -m 0755 $(BINDIR)/spex-probe $(DESTDIR)$(PREFIX)/bin/spex-probe
	install -m 0755 $(BINDIR)/spex-probe-redis $(DESTDIR)$(PREFIX)/bin/spex-probe-redis
	install -m 0755 $(BINDIR)/spex-demo-stack $(DESTDIR)$(PREFIX)/bin/spex-demo-stack

release: build
	mkdir -p $(DISTDIR)
	install -m 0755 $(BINDIR)/spex $(DISTDIR)/spex
	install -m 0755 $(BINDIR)/spex-probe $(DISTDIR)/spex-probe
	install -m 0755 $(BINDIR)/spex-probe-redis $(DISTDIR)/spex-probe-redis
	install -m 0755 $(BINDIR)/spex-demo-stack $(DISTDIR)/spex-demo-stack
	install -m 0644 LICENSE $(DISTDIR)/LICENSE
	install -m 0644 COMMERCIAL.md $(DISTDIR)/COMMERCIAL.md
	install -m 0644 CONTRIBUTING.md $(DISTDIR)/CONTRIBUTING.md
	install -m 0644 THIRD-PARTY-NOTICES.md $(DISTDIR)/THIRD-PARTY-NOTICES.md
	$(GO_TOOL) list -m all > $(DISTDIR)/go-modules.txt
	{ printf '%s\n' 'spex'; $(GO_TOOL) version -m $(DISTDIR)/spex | sed '1d'; } > $(DISTDIR)/buildinfo.txt
	{ \
	  printf '%s\n' '# third-party licenses'; \
	  $(GO_TOOL) list -m -f '{{if .Version}}{{.Path}} {{.Version}} {{.Dir}}{{end}}' all | while read module version dir; do \
	    test -n "$$module" || continue; \
	    printf '\nmodule %s %s\n' "$$module" "$$version"; \
	    found=false; \
	    for license in "$$dir"/LICENSE* "$$dir"/COPYING* "$$dir"/NOTICE*; do \
	      if [ -f "$$license" ]; then printf 'license-file %s\n' "$${license##*/}"; found=true; fi; \
	    done; \
	    if [ "$$found" = false ]; then printf 'license-file none-detected\n'; fi; \
	  done; \
	} > $(DISTDIR)/third-party-licenses.txt
	chmod 0644 $(DISTDIR)/go-modules.txt $(DISTDIR)/buildinfo.txt $(DISTDIR)/third-party-licenses.txt
	$(DISTDIR)/spex release module-inventory --dist $(DISTDIR)
	$(DISTDIR)/spex release provenance --dist $(DISTDIR)
	$(DISTDIR)/spex release checksum --dist $(DISTDIR)
	$(DISTDIR)/spex version --format json > $(DISTDIR)/version.json
	chmod 0644 $(DISTDIR)/version.json
	$(DISTDIR)/spex release manifest --dist $(DISTDIR)

release-check:
	rm -rf $(RELEASE_CHECK_DISTDIR)
	$(MAKE) release DISTDIR=$(RELEASE_CHECK_DISTDIR) VERSION=$(VERSION) COMMIT=$(COMMIT) BUILD_DATE=$(BUILD_DATE)
	test -f $(RELEASE_CHECK_DISTDIR)/spex
	test -f $(RELEASE_CHECK_DISTDIR)/spex-probe
	test -f $(RELEASE_CHECK_DISTDIR)/spex-probe-redis
	test -f $(RELEASE_CHECK_DISTDIR)/spex-demo-stack
	test -f $(RELEASE_CHECK_DISTDIR)/LICENSE
	test -f $(RELEASE_CHECK_DISTDIR)/COMMERCIAL.md
	test -f $(RELEASE_CHECK_DISTDIR)/CONTRIBUTING.md
	test -f $(RELEASE_CHECK_DISTDIR)/THIRD-PARTY-NOTICES.md
	test -f $(RELEASE_CHECK_DISTDIR)/dependency-inventory.json
	test -f $(RELEASE_CHECK_DISTDIR)/SHA256SUMS
	test -f $(RELEASE_CHECK_DISTDIR)/version.json
	test -f $(RELEASE_CHECK_DISTDIR)/release-manifest.yaml
	grep -q '"version": "$(VERSION)"' $(RELEASE_CHECK_DISTDIR)/version.json
	$(RELEASE_CHECK_DISTDIR)/spex release verify --dist $(RELEASE_CHECK_DISTDIR)

release-archive: release
	$(DISTDIR)/spex release archive --dist $(DISTDIR) --name $(ARCHIVE_NAME).tar.gz $(RELEASE_ARCHIVE_FLAGS)
	$(DISTDIR)/spex release checksum --archive $(DISTDIR)/$(ARCHIVE_NAME).tar.gz
	$(DISTDIR)/spex release verify --dist $(DISTDIR) --archive $(DISTDIR)/$(ARCHIVE_NAME).tar.gz

release-archive-check:
	rm -rf $(RELEASE_CHECK_DISTDIR)
	$(MAKE) release-archive DISTDIR=$(RELEASE_CHECK_DISTDIR) VERSION=$(VERSION) COMMIT=$(COMMIT) BUILD_DATE=$(BUILD_DATE) RELEASE_ARCHIVE_FLAGS=--force
	test -f $(RELEASE_CHECK_DISTDIR)/$(ARCHIVE_NAME).tar.gz
	test -f $(RELEASE_CHECK_DISTDIR)/$(ARCHIVE_NAME).tar.gz.sha256

production-check:
	$(GO_RUN) ./cmd/spex doctor --suite $(SUITE) --skip-host-tools --require-pinned-git-refs $(if $(filter true,$(REQUIRE_PINNED_IMAGES)),--require-pinned-images,) $(foreach dir,$(wildcard $(PRODUCTION_ARTIFACTS)),--scan-artifacts $(dir)) --format json

reference-scenario-check:
	$(MAKE) -C examples/reference-scenario-repo ci
	SPEX_PRODUCTION_CHECK=true $(MAKE) -C examples/reference-scenario-repo ci

production-candidate-check: verify production-check reference-scenario-check

live-proof-kind: probe-image
	INTEGRATION_PROFILE=examples/integration/local-kind-profile.yaml KUBE_CONTEXT=kind-kind PROBE_IMAGE=$${PROBE_IMAGE:-spex-probe:dev} PROBE_IMAGE_PULL_POLICY=IfNotPresent WORKSPACE=generated/mqtt-ingestion-basic-live scripts/integration_live.sh

live-proof-keycloak: probe-image
	BINDING=examples/bindings/local-dev-keycloak.yaml INTEGRATION_PROFILE=examples/integration/local-kind-keycloak-profile.yaml WORKSPACE=generated/mqtt-ingestion-basic-keycloak-live KUBE_CONTEXT=kind-kind PROBE_IMAGE=$${PROBE_IMAGE:-spex-probe:dev} PROBE_IMAGE_PULL_POLICY=IfNotPresent scripts/integration_live.sh

probe-image:
	docker build -f examples/integration/probe/Dockerfile -t $${PROBE_IMAGE:-spex-probe:dev} .

redis-probe-image:
	docker build -f examples/integration/probe-redis/Dockerfile -t $${REDIS_PROBE_IMAGE:-spex-probe-redis:dev} .

spex-status:
	scripts/spex_status.sh

compile-example:
	$(GO_RUN) ./cmd/spex compile --scenario examples/scenarios/mqtt-ingestion-basic.yaml --binding examples/bindings/local-dev.yaml --out generated/mqtt-ingestion-basic --run-id run-fixed-test

compile-example-keycloak:
	$(GO_RUN) ./cmd/spex compile --scenario examples/scenarios/mqtt-ingestion-basic.yaml --binding examples/bindings/local-dev-keycloak.yaml --out generated/mqtt-ingestion-basic-keycloak --run-id run-fixed-test

compile-example-kind:
	$(GO_RUN) ./cmd/spex compile --scenario examples/scenarios/mqtt-ingestion-basic.yaml --binding examples/bindings/local-dev.yaml --integration-profile examples/integration/local-kind-profile.yaml --out generated/mqtt-ingestion-basic-kind --run-id run-fixed-test --kube-context kind-kind --probe-image $${PROBE_IMAGE:-spex-probe:dev} --probe-image-pull-policy IfNotPresent --repo-root '$${REPO_ROOT:-../..}'

compile-example-kind-keycloak:
	$(GO_RUN) ./cmd/spex compile --scenario examples/scenarios/mqtt-ingestion-basic.yaml --binding examples/bindings/local-dev-keycloak.yaml --integration-profile examples/integration/local-kind-keycloak-profile.yaml --out generated/mqtt-ingestion-basic-kind-keycloak --run-id run-fixed-test --kube-context kind-kind --probe-image $${PROBE_IMAGE:-spex-probe:dev} --probe-image-pull-policy IfNotPresent --repo-root '$${REPO_ROOT:-../..}'

validate-suite-example:
	$(GO_RUN) ./cmd/spex suite validate --suite examples/suites/mqtt-local.yaml
	$(GO_RUN) ./cmd/spex suite validate --suite examples/suites/mqtt-local-keycloak.yaml --kube-context kind-kind --probe-image $${PROBE_IMAGE:-spex-probe:dev} --repo-root '$${REPO_ROOT:-../..}'

compile-suite-example:
	$(GO_RUN) ./cmd/spex suite compile --suite examples/suites/mqtt-local.yaml --out generated/suites/mqtt-local --run-id suite-fixed-test

run-example-noop:
	$(GO_RUN) ./cmd/spex run --workspace generated/mqtt-ingestion-basic --command /usr/bin/true

clean-example:
	$(GO_RUN) ./cmd/spex clean --workspace generated/mqtt-ingestion-basic --command /bin/echo

smoke-probes:
	$(GO_RUN) ./cmd/spex-probe graphql expect --query-file examples/queries/latest-device-reading.graphql --variables-file examples/matchers/assert-reading-1-in-graphql.variables.json --matchers-file examples/matchers/assert-reading-1-in-graphql.matchers.json --fixture-response-file examples/fixtures/graphql-response.json
	$(GO_RUN) ./cmd/spex-probe redpanda contains --offsets-file /dev/null --matchers-file generated/mqtt-ingestion-basic/rendered/matchers/assert-reading-1-in-redpanda.matchers.json --fixture-event-file examples/fixtures/redpanda-event.json

smoke-integration-script:
	sh -n scripts/integration_live.sh
	mkdir -p $(SMOKE_DIR)
	scripts/spex_status.sh >/tmp/spex-status-smoke.out; grep -Eq "live proof: (ready to attempt|complete)" /tmp/spex-status-smoke.out
	$(GO_RUN) ./cmd/spex suite validate --suite examples/suites/mqtt-local.yaml
	! grep -R -E '/Users/|/private/tmp' examples/stacks/generated
	! grep -R -F '$$$${REPO_ROOT' examples/stacks/generated
	$(GO_RUN) ./cmd/spex suite plan --suite examples/suites/mqtt-local.yaml --format json >/tmp/spex-suite-plan-smoke.json; grep -q '"suite": "mqtt-local"' /tmp/spex-suite-plan-smoke.json; grep -q '"requiredSecrets"' /tmp/spex-suite-plan-smoke.json
	$(GO_RUN) ./cmd/spex suite compile --suite examples/suites/mqtt-local.yaml --out $(SMOKE_DIR)/suite-smoke --run-id suite-smoke
	$(GO_RUN) ./cmd/spex suite list --suite examples/suites/mqtt-local.yaml >/tmp/spex-suite-list-smoke.out; grep -q "mqtt-ingestion-flow" /tmp/spex-suite-list-smoke.out; grep -q "mqtt-reading-reaches-redpanda-and-graphql" /tmp/spex-suite-list-smoke.out
	$(GO_RUN) ./cmd/spex suite list --suite examples/suites/mqtt-local.yaml --format json >/tmp/spex-suite-list-smoke.json; grep -q '"suite": "mqtt-local"' /tmp/spex-suite-list-smoke.json; grep -q '"name": "mqtt-ingestion-flow"' /tmp/spex-suite-list-smoke.json
	$(GO_RUN) ./cmd/spex suite explain --suite examples/suites/mqtt-local.yaml >/tmp/spex-suite-explain-smoke.out; grep -q "scenario: mqtt-ingestion-flow" /tmp/spex-suite-explain-smoke.out; grep -q "scenario: mqtt-reading-reaches-redpanda-and-graphql" /tmp/spex-suite-explain-smoke.out
	$(GO_RUN) ./cmd/spex suite explain --suite examples/suites/mqtt-local.yaml --format json >/tmp/spex-suite-explain-smoke.json; grep -q '"suite": "mqtt-local"' /tmp/spex-suite-explain-smoke.json; grep -q '"name": "mqtt-ingestion-flow"' /tmp/spex-suite-explain-smoke.json
	$(GO_RUN) ./cmd/spex catalog list --suite examples/suites/mqtt-local.yaml >/tmp/spex-catalog-list-smoke.out; grep -q "mqttToRedpandaToGraphql" /tmp/spex-catalog-list-smoke.out; grep -q 'when "device' /tmp/spex-catalog-list-smoke.out
	$(GO_RUN) ./cmd/spex catalog list --suite examples/suites/mqtt-local.yaml --format json >/tmp/spex-catalog-list-smoke.json; grep -q '"name": "mqttToRedpandaToGraphql"' /tmp/spex-catalog-list-smoke.json; grep -q '"kind": "when"' /tmp/spex-catalog-list-smoke.json
	$(GO_RUN) ./cmd/spex catalog explain --suite examples/suites/mqtt-local.yaml >/tmp/spex-catalog-explain-smoke.out; grep -q "operations:" /tmp/spex-catalog-explain-smoke.out; grep -q "payloadTemplates:" /tmp/spex-catalog-explain-smoke.out; grep -q "graphqlQueries:" /tmp/spex-catalog-explain-smoke.out
	$(GO_RUN) ./cmd/spex catalog explain --suite examples/suites/mqtt-local.yaml --format json >/tmp/spex-catalog-explain-smoke.json; grep -q '"operationCount": 3' /tmp/spex-catalog-explain-smoke.json; grep -q '"payloadTemplateCount": 1' /tmp/spex-catalog-explain-smoke.json; grep -q '"graphqlQueryCount": 1' /tmp/spex-catalog-explain-smoke.json
	$(GO_RUN) ./cmd/spex catalog check --suite examples/suites/mqtt-local.yaml >/tmp/spex-catalog-check-smoke.out; grep -q "catalog check passed" /tmp/spex-catalog-check-smoke.out
	$(GO_RUN) ./cmd/spex catalog check --suite examples/suites/mqtt-local.yaml --format json >/tmp/spex-catalog-check-smoke.json; grep -q '"status": "passed"' /tmp/spex-catalog-check-smoke.json; grep -q '"steps": 5' /tmp/spex-catalog-check-smoke.json
	$(GO_RUN) ./cmd/spex catalog docs --suite examples/suites/mqtt-local.yaml >/tmp/spex-catalog-docs-smoke.md; grep -q "# spex Catalog" /tmp/spex-catalog-docs-smoke.md
	$(GO_RUN) ./cmd/spex schema list >/tmp/spex-schema-list-smoke.out; grep -q "scenario-suite" /tmp/spex-schema-list-smoke.out; grep -q "target-binding" /tmp/spex-schema-list-smoke.out
	$(GO_RUN) ./cmd/spex schema list --format json >/tmp/spex-schema-list-smoke.json; grep -q '"schemas"' /tmp/spex-schema-list-smoke.json; grep -q '"scenario-suite"' /tmp/spex-schema-list-smoke.json
	$(GO_RUN) ./cmd/spex schema show scenario >/tmp/spex-schema-scenario-smoke.json; grep -q '"title": "spex Scenario"' /tmp/spex-schema-scenario-smoke.json
	$(GO_RUN) ./cmd/spex help >/tmp/spex-help-smoke.out; grep -q "usage: spex" /tmp/spex-help-smoke.out; grep -q "init scenario-repo" /tmp/spex-help-smoke.out
	$(GO_RUN) ./cmd/spex suite help >/tmp/spex-suite-help-smoke.out; grep -q "usage: spex suite" /tmp/spex-suite-help-smoke.out; grep -q "suite run" /tmp/spex-suite-help-smoke.out
	$(GO_RUN) ./cmd/spex version --format json >/tmp/spex-version-smoke.json; grep -q '"version": "0.1.0-dev"' /tmp/spex-version-smoke.json
	$(GO_RUN) ./cmd/spex suite run --suite examples/suites/mqtt-local.yaml --out $(SMOKE_DIR)/suite-run-smoke --run-id suite-run-smoke --command /usr/bin/true --collect-resource-usage >/tmp/spex-suite-run-smoke.out; test -f $(SMOKE_DIR)/suite-run-smoke/reports/suite-junit.xml; test -f $(SMOKE_DIR)/suite-run-smoke/reports/suite-run-report.yaml; test -f $(SMOKE_DIR)/suite-run-smoke/reports/suite-run-report.json; test -f $(SMOKE_DIR)/suite-run-smoke/mqtt-ingestion-basic/reports/scenario-run-report.json
	rm -rf $(SMOKE_DIR)/scenario-repo-smoke; $(GO_RUN) ./cmd/spex init scenario-repo --dir $(SMOKE_DIR)/scenario-repo-smoke; test -f $(SMOKE_DIR)/scenario-repo-smoke/.schemas/scenario.schema.json; test -f $(SMOKE_DIR)/scenario-repo-smoke/.vscode/settings.json; test -x $(SMOKE_DIR)/scenario-repo-smoke/ci/spex-validate.sh; test -f $(SMOKE_DIR)/scenario-repo-smoke/.github/workflows/spex.yaml; test -f $(SMOKE_DIR)/scenario-repo-smoke/Makefile; grep -q "suite validate" $(SMOKE_DIR)/scenario-repo-smoke/Makefile; grep -q "spex-validate.sh" $(SMOKE_DIR)/scenario-repo-smoke/.github/workflows/spex.yaml; cd $(SMOKE_DIR)/scenario-repo-smoke; make help >/tmp/spex-scenario-repo-help.out; grep -q "make validate" /tmp/spex-scenario-repo-help.out; cd - >/dev/null; $(GO_RUN) ./cmd/spex suite validate --suite $(SMOKE_DIR)/scenario-repo-smoke/suite.yaml
	$(GO_BUILD) -o $(SMOKE_DIR)/scenario-repo-smoke/spex ./cmd/spex
	cd $(SMOKE_DIR)/scenario-repo-smoke; SPEX=$(SMOKE_DIR)/scenario-repo-smoke/spex make ci; test -f reports/schema-list.json; grep -q '"schemas"' reports/schema-list.json; test -f reports/suite-list.json; grep -q '"suite": "device-acceptance"' reports/suite-list.json; test -f reports/suite-plan.json; grep -q '"suite": "device-acceptance"' reports/suite-plan.json; test -f reports/suite-explain.txt; grep -q "scenario: mqtt-ingestion-basic" reports/suite-explain.txt; test -f reports/suite-explain.json; grep -q '"name": "mqtt-ingestion-basic"' reports/suite-explain.json; test -f reports/catalog-list.json; grep -q '"flows"' reports/catalog-list.json; test -f reports/catalog-explain.json; grep -q '"operationCount": 3' reports/catalog-explain.json; grep -q '"payloadTemplateCount": 1' reports/catalog-explain.json; test -f reports/catalog-check.json; grep -q '"status": "passed"' reports/catalog-check.json; test -f reports/catalog.md; grep -q "# spex Catalog" reports/catalog.md; grep -q "Expansion:" reports/catalog.md
	cd $(SMOKE_DIR)/scenario-repo-smoke; SPEX=$(SMOKE_DIR)/scenario-repo-smoke/spex SPEX_PRODUCTION_CHECK=true make ci; test -f reports/production-check.json; grep -q '"status": "passed"' reports/production-check.json; grep -q '"artifactSecretScan:reports"' reports/production-check.json; grep -q '"artifactSecretScan:generated/ci"' reports/production-check.json
	$(SMOKE_DIR)/scenario-repo-smoke/spex new scenario --dir $(SMOKE_DIR)/scenario-repo-smoke --name flow-smoke --style flow; test -f $(SMOKE_DIR)/scenario-repo-smoke/scenarios/flow-smoke.yaml; $(SMOKE_DIR)/scenario-repo-smoke/spex new scenario --dir $(SMOKE_DIR)/scenario-repo-smoke --name feature-smoke --style feature; test -f $(SMOKE_DIR)/scenario-repo-smoke/features/feature-smoke.feature
	cd $(SMOKE_DIR)/scenario-repo-smoke; SPEX=$(SMOKE_DIR)/scenario-repo-smoke/spex make ci; test -f reports/schema-list.json; grep -q '"schemas"' reports/schema-list.json; test -f reports/suite-list.json; grep -q '"suite": "device-acceptance"' reports/suite-list.json; test -f reports/suite-plan.json; grep -q '"suite": "device-acceptance"' reports/suite-plan.json; test -f reports/suite-explain.txt; grep -q "scenario: mqtt-ingestion-basic" reports/suite-explain.txt; test -f reports/suite-explain.json; grep -q '"name": "mqtt-ingestion-basic"' reports/suite-explain.json; test -f reports/catalog-list.json; grep -q '"flows"' reports/catalog-list.json; test -f reports/catalog-explain.json; grep -q '"operationCount": 3' reports/catalog-explain.json; grep -q '"payloadTemplateCount": 1' reports/catalog-explain.json; test -f reports/catalog-check.json; grep -q '"status": "passed"' reports/catalog-check.json; test -f reports/catalog.md; grep -q "# spex Catalog" reports/catalog.md
	$(GO_RUN) ./cmd/spex validate --scenario examples/scenarios/mqtt-ingestion-basic.yaml --binding examples/bindings/local-dev.yaml --integration-profile examples/integration/local-kind-profile.yaml --kube-context kind-kind
	if $(GO_RUN) ./cmd/spex validate --scenario examples/scenarios/mqtt-ingestion-basic.yaml --binding examples/bindings/local-dev.yaml --integration-profile examples/integration/local-kind-profile.yaml --kube-context kind-other >/tmp/spex-profile-context-smoke.out 2>&1; then exit 1; fi; grep -q 'requires kubeContext "kind-kind"' /tmp/spex-profile-context-smoke.out
	INTEGRATION_CONFIG=$(SMOKE_DIR)/missing-config.env scripts/integration_live.sh >/tmp/spex-config-smoke.out 2>&1; test $$? -eq 2; grep -q "INTEGRATION_CONFIG does not exist" /tmp/spex-config-smoke.out
	INTEGRATION_CONFIG=examples/integration/local-kind.env.example INTEGRATION_RUN_KUTTL=false KUBECTL=true scripts/integration_live.sh >/tmp/spex-config-example-smoke.out; grep -q "workspace compiled:" /tmp/spex-config-example-smoke.out
	INTEGRATION_PROFILE=$(SMOKE_DIR)/missing-profile.yaml scripts/integration_live.sh >/tmp/spex-profile-smoke.out 2>&1; test $$? -eq 2; grep -q "INTEGRATION_PROFILE does not exist" /tmp/spex-profile-smoke.out
	printf '%s\n' 'KUBECTL=/bin/true$$(touch /tmp/spex-eval-smoke)' >/tmp/spex-eval-smoke.env; rm -f /tmp/spex-eval-smoke; INTEGRATION_CONFIG=/tmp/spex-eval-smoke.env scripts/integration_live.sh >/tmp/spex-eval-smoke.out 2>&1; test $$? -eq 2; test ! -e /tmp/spex-eval-smoke
	printf '%s\n' 'apiVersion: spex.integration.v0.1' 'kind: IntegrationProfile' 'spec:' '  setup:' '    commands:' '      - command: helm install placeholder oci://registry.example.com/platform/graphql-api' >/tmp/spex-placeholder-profile.yaml; INTEGRATION_PROFILE=/tmp/spex-placeholder-profile.yaml KUBECTL=true scripts/integration_live.sh >/tmp/spex-placeholder-profile-smoke.out 2>&1; test $$? -eq 2; grep -q "contains placeholder chart/image coordinates" /tmp/spex-placeholder-profile-smoke.out
	SPEX_MQTT_USERNAME= SPEX_MQTT_PASSWORD= SPEX_GRAPHQL_TOKEN= INTEGRATION_PROFILE=examples/integration/local-kind-profile.yaml KUBE_CONTEXT=kind-kind KUBECTL=true scripts/integration_live.sh >/tmp/spex-profile-env-smoke.out 2>&1; test $$? -eq 2; grep -q 'SPEX_.* is not set' /tmp/spex-profile-env-smoke.out
	printf '%s\n' 'apiVersion: spex.integration.v0.1' 'kind: IntegrationProfile' 'spec:' '  setup:' '    commands:' '      - command: test -n "$$SPEX_FUTURE_SECRET"' >/tmp/spex-future-env-profile.yaml; INTEGRATION_PROFILE=/tmp/spex-future-env-profile.yaml KUBECTL=true scripts/integration_live.sh >/tmp/spex-future-env-smoke.out 2>&1; test $$? -eq 2; grep -q 'SPEX_FUTURE_SECRET is not set' /tmp/spex-future-env-smoke.out
	printf '%s\n' 'apiVersion: spex.integration.v0.1' 'kind: IntegrationProfile' 'spec:' '  setup:' '    commands:' '      - command: helm version' >/tmp/spex-host-tool-profile.yaml; PATH=/usr/bin:/bin SPEX=/bin/true INTEGRATION_PROFILE=/tmp/spex-host-tool-profile.yaml KUBECTL=true scripts/integration_live.sh >/tmp/spex-host-tool-smoke.out 2>&1; test $$? -eq 2; grep -q 'missing required command: helm' /tmp/spex-host-tool-smoke.out
	INTEGRATION_RUN_KUTTL=false INTEGRATION_PROFILE=examples/integration/local-kind-profile.yaml KUBE_CONTEXT=kind-kind WORKSPACE=$(SMOKE_DIR)/compile-only-workspace KUBECTL=$(SMOKE_DIR)/missing-kubectl scripts/integration_live.sh >/tmp/spex-compile-only-smoke.out 2>&1; grep -q "workspace compiled:" /tmp/spex-compile-only-smoke.out

integration-example:
	scripts/integration_live.sh

integration-example-keycloak:
	BINDING=examples/bindings/local-dev-keycloak.yaml WORKSPACE=generated/mqtt-ingestion-basic-keycloak-integration scripts/integration_live.sh

integration-example-kind-keycloak:
	BINDING=examples/bindings/local-dev-keycloak.yaml INTEGRATION_PROFILE=examples/integration/local-kind-keycloak-profile.yaml WORKSPACE=generated/mqtt-ingestion-basic-kind-keycloak-integration KUBE_CONTEXT=kind-kind PROBE_IMAGE=$${PROBE_IMAGE:-spex-probe:dev} PROBE_IMAGE_PULL_POLICY=IfNotPresent scripts/integration_live.sh
