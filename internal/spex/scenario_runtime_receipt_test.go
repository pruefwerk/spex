package spex

import (
	"strings"
	"testing"

	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

func TestResolvedRuntimeReceipt(t *testing.T) {
	id := strings.Repeat("c", 64)
	receipt := remoteReceipt{Schema: "spex.remote-result/v2", ResolvedScenarioID: id, RuntimeRelease: "v0.4.0", Result: scenarioruntime.ExecutionResult{ScenarioID: id, Runtime: "migration-testbench/v1", RuntimeRelease: "v0.4.0"}}
	for _, selector := range []string{"", "latest", "migration-testbench/v1", "migration-testbench/v1@latest", "migration-testbench/v1@v0.4.0"} {
		if err := validateRuntimeReceipt(receipt, remoteRequest{Schema: "spex.submission/v2", Runtime: selector}); err != nil {
			t.Fatalf("%q: %v", selector, err)
		}
	}
	for _, selector := range []string{"other/v1", "migration-testbench/v1@v0.3.0"} {
		if err := validateRuntimeReceipt(receipt, remoteRequest{Schema: "spex.submission/v2", Runtime: selector}); err == nil {
			t.Fatal("accepted fallback for explicit selector")
		}
	}
	for _, mutate := range []func(*remoteReceipt){
		func(r *remoteReceipt) { r.RuntimeRelease = "" },
		func(r *remoteReceipt) { r.RuntimeRelease = "latest"; r.Result.RuntimeRelease = "latest" },
		func(r *remoteReceipt) { r.Result.RuntimeRelease = "v9" },
		func(r *remoteReceipt) { r.ResolvedScenarioID = strings.Repeat("d", 64) },
		func(r *remoteReceipt) { r.Schema = "spex.remote-result/v1" },
	} {
		bad := receipt
		mutate(&bad)
		if err := validateRuntimeReceipt(bad, remoteRequest{Schema: "spex.submission/v2"}); err == nil {
			t.Fatal("accepted unresolved or inconsistent receipt")
		}
	}
}
