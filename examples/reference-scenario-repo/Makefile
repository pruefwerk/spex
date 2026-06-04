ROOT ?= ../..
SPEX ?= env GOCACHE=$(abspath $(ROOT))/.cache/go-build go run ./cmd/spex
SUITE ?= examples/reference-scenario-repo/suite.yaml
LIVE_SUITE ?= examples/reference-scenario-repo/suite-keycloak.yaml
OUT ?= examples/reference-scenario-repo/generated/ci
REPORTS ?= examples/reference-scenario-repo/reports
RUN_ID ?= reference-ci
PROBE_IMAGE ?= spex-probe:dev
PROBE_IMAGE_PULL_POLICY ?= IfNotPresent
KUBE_CONTEXT ?= kind-kind
BUNDLE_LOCK ?=

.PHONY: help validate plan explain catalog catalog-docs compile production-check ci live clean

help:
	@printf '%s\n' 'reference scenario repo targets:'
	@printf '%s\n' '  make ci'
	@printf '%s\n' '  SPEX_PRODUCTION_CHECK=true make ci'
	@printf '%s\n' '  make live'

validate:
	cd $(ROOT) && $(SPEX) suite validate --suite $(SUITE) --kube-context $(KUBE_CONTEXT) --probe-image $(PROBE_IMAGE) --probe-image-pull-policy $(PROBE_IMAGE_PULL_POLICY) --repo-root .

plan:
	cd $(ROOT) && mkdir -p $(REPORTS)
	cd $(ROOT) && $(SPEX) suite plan --suite $(SUITE) --kube-context $(KUBE_CONTEXT) --probe-image $(PROBE_IMAGE) --probe-image-pull-policy $(PROBE_IMAGE_PULL_POLICY) --repo-root . --format json > $(REPORTS)/suite-plan.json

explain:
	cd $(ROOT) && mkdir -p $(REPORTS)
	cd $(ROOT) && $(SPEX) suite explain --suite $(SUITE) --format json > $(REPORTS)/suite-explain.json

catalog:
	cd $(ROOT) && mkdir -p $(REPORTS)
	cd $(ROOT) && $(SPEX) catalog check --suite $(SUITE) --format json > $(REPORTS)/catalog-check.json

catalog-docs:
	cd $(ROOT) && mkdir -p $(REPORTS)
	cd $(ROOT) && $(SPEX) catalog docs --suite $(SUITE) --out $(REPORTS)/catalog.md

compile:
	cd $(ROOT) && $(SPEX) suite compile --suite $(SUITE) --out $(OUT) --run-id $(RUN_ID) --kube-context $(KUBE_CONTEXT) --probe-image $(PROBE_IMAGE) --probe-image-pull-policy $(PROBE_IMAGE_PULL_POLICY) --repo-root .

production-check:
	cd $(ROOT) && mkdir -p $(REPORTS)
	@if [ -n "$(BUNDLE_LOCK)" ]; then cd $(ROOT) && $(SPEX) bundle verify --suite $(SUITE) --lock $(BUNDLE_LOCK); fi
	cd $(ROOT) && $(SPEX) doctor --suite $(SUITE) --skip-host-tools --require-pinned-git-refs --scan-artifacts $(REPORTS) --scan-artifacts $(OUT) --format json > $(REPORTS)/production-check.json

ci: validate plan explain catalog catalog-docs compile
	@if [ "$${SPEX_PRODUCTION_CHECK:-false}" = "true" ]; then $(MAKE) production-check; fi

live:
	cd $(ROOT) && $(SPEX) suite run --suite $(LIVE_SUITE) --out examples/reference-scenario-repo/generated/live --run-id live --kube-context $(KUBE_CONTEXT) --probe-image $(PROBE_IMAGE) --probe-image-pull-policy $(PROBE_IMAGE_PULL_POLICY) --repo-root . --collect-resource-usage

clean:
	rm -rf generated reports
