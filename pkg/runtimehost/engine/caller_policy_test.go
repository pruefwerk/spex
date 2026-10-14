package engine

import (
	"context"
	"testing"

	"github.com/pruefwerk/spex/pkg/scenario"
)

func TestCallerAdmissionPreservesDefaultGuardrails(t *testing.T) {
	_, file := configuredFixture(t)
	h, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	p := &policy{host: h, release: "v1.0.0"}
	inline := "Feature: Caller\n Scenario: Smoke\n Given shared step\n"
	doc := scenario.Scenario{Schema: scenario.Schema, Runtime: h.config.Runtime, RuntimeRelease: p.release, Tests: []scenario.TestSource{{Inline: &inline}}}
	if err := p.Allow(context.Background(), doc); err == nil {
		t.Fatal("default host accepted caller tests")
	}
	h.config.CallerTests = true
	if err := p.Allow(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		config string
		fields []string
		pass   bool
	}{
		{"[configuration]\ncatalogs=['steps.yaml']", nil, false},
		{"[configuration]\ncatalogs=['steps.yaml']", []string{"configuration.catalogs"}, true},
		{"[configuration]\nbinding='binding.yaml'", []string{"configuration.catalogs"}, false},
		{"suite='caller-suite.yaml'", []string{"configuration.catalogs"}, false},
		{"[execution]\nretain_runtime_resources=true", nil, false},
		{"[selection]\ninclude_tags=['unapproved']", nil, false},
	} {
		doc.RuntimeConfig, err = scenario.ParseRuntimeConfig([]byte(tc.config))
		if err != nil {
			t.Fatal(err)
		}
		h.config.CallerConfiguration = tc.fields
		if err := p.Allow(context.Background(), doc); (err == nil) != tc.pass {
			t.Fatalf("%s admitted=%v expected=%v", tc.config, err, tc.pass)
		}
	}
}
