package spex

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/pkg/scenario"
)

func TestSplitInlineSources(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        []string
	}{
		{"single", "Feature: One\n", []string{"Feature: One\n"}},
		{"mixed", "Feature: One\n---\nkind: Scenario\n---\nFeature: Two", []string{"Feature: One\n", "kind: Scenario\n", "Feature: Two"}},
		{"opening marker", "---\nkind: Scenario\n", []string{"kind: Scenario\n"}},
		{"line endings", "Feature: One\r\n--- \t\r\nFeature: Two\r", []string{"Feature: One\n", "Feature: Two\n"}},
		{"block scalar", "body: |\n  ---\n  text\n", []string{"body: |\n  ---\n  text\n"}},
		{"docstring", "Feature: One\n  Scenario: Example\n    Given input\n      \"\"\"\n      ---\n      \"\"\"\n", []string{"Feature: One\n  Scenario: Example\n    Given input\n      \"\"\"\n      ---\n      \"\"\"\n"}},
		{"not standalone", "Feature: One\n---suffix\n", []string{"Feature: One\n---suffix\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitInlineSources(tc.input)
			if err != nil || len(got) != len(tc.want) {
				t.Fatalf("sources=%v err=%v", got, err)
			}
			for i := range got {
				if *got[i].Inline != tc.want[i] {
					t.Errorf("source %d changed: %q", i, *got[i].Inline)
				}
			}
		})
	}
	for _, input := range []string{"", "---\n", "Feature: One\n---\n", "Feature: One\n---\n---\nFeature: Two"} {
		if _, err := splitInlineSources(input); err == nil || !strings.Contains(err.Error(), "line ") {
			t.Errorf("expected located empty-source error for %q: %v", input, err)
		}
	}
}

func TestInlineDocumentsPackageAsExplicitSources(t *testing.T) {
	root := t.TempDir()
	text := "Feature: One\n---\n" + scenarioSmokeSource
	if err := os.WriteFile(filepath.Join(root, "sources.txt"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Run([]string{"scenario", "package", "--workspace", root, "--runtime", "migration-testbench/v1", "--inline-file", "sources.txt", "--out", "out/scenario.toml"}, &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "out/scenario.toml"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := scenario.ParseRequest(data)
	if err != nil || len(doc.Tests) != 2 {
		t.Fatalf("document=%v err=%v", doc, err)
	}
	if *doc.Tests[0].Inline != "Feature: One\n" || *doc.Tests[1].Inline != scenarioSmokeSource {
		t.Fatal("source contents changed")
	}
	want, err := scenario.SerializeRequest(scenario.Scenario{Schema: scenario.RequestSchema, Runtime: "migration-testbench/v1", Tests: doc.Tests})
	if err != nil || !bytes.Equal(data, want) {
		t.Fatal("package did not preserve canonical sources")
	}
}
