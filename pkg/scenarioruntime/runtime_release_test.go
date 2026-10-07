package scenarioruntime

import (
	"context"
	"testing"
)

type releaseFake struct {
	*fake
	version string
}

func (f releaseFake) Release() string { return f.version }

func TestPinnedRuntimeRequiresMatchingImplementation(t *testing.T) {
	for _, version := range []string{"", "v1.0.0", "v2.0.0"} {
		f := &fake{result: ExecutionResult{Outcome: Passed}}
		registry := NewRegistry()
		var implementation Runtime = f
		if version != "" {
			implementation = releaseFake{f, version}
		}
		if err := registry.Register(implementation); err != nil {
			t.Fatal(err)
		}
		req := request()
		req.Scenario.RuntimeRelease = "v1.0.0"
		prepared, err := registry.Prepare(context.Background(), req)
		if version != "v1.0.0" {
			if err == nil || len(f.calls) != 0 {
				t.Fatal("unavailable release reached runtime resolution")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		result, err := prepared.Execute(context.Background(), nil)
		if err != nil || result.RuntimeRelease != "v1.0.0" {
			t.Fatal("result lost runtime release")
		}
	}
}
