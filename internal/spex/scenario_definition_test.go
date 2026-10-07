package spex

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/pkg/scenario"
)

func TestUnifiedDefinitions(t *testing.T) {
	root := t.TempDir()
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	toml := "schema='spex.scenario/v1'\nruntime='migration-testbench/v1'\ndependencies=['fixture.json']\n[[tests]]\nfile='one.feature'\n"
	write("fixture.json", "{}")
	write("one.feature", "Feature: One\n")
	write("two.yaml", scenarioSmokeSource)
	write("scenario.toml", toml)
	write("toml-inline.txt", toml)
	write("mixed-inline.txt", "Feature: One\n---\n"+scenarioSmokeSource)
	write("unknown.txt", "some unrecognized text")
	write("mixed-toml.txt", toml+"---\nFeature: Two\n")
	write("multi.yaml", scenarioSmokeSource+"---\n"+scenarioSmokeSource)
	write("bad-dependency.toml", strings.ReplaceAll(toml, "fixture.json", "missing.json"))
	write("escaping-dependency.toml", strings.ReplaceAll(toml, "fixture.json", "../outside.json"))
	for i, tc := range []struct {
		args       []string
		count      int
		dependency bool
	}{
		{[]string{"--definition-file", "one.feature", "--definition-file", "two.yaml"}, 2, false},
		{[]string{"--definition-inline-file", "mixed-inline.txt"}, 2, false},
		{[]string{"--definition-file", "scenario.toml"}, 1, true},
		{[]string{"--definition-inline-file", "toml-inline.txt"}, 1, true},
	} {
		var out bytes.Buffer
		destination := filepath.Join("out", string(rune('a'+i)), "scenario.toml")
		args := append([]string{"scenario", "package", "--workspace", root, "--out", destination}, tc.args...)
		if err := Run(args, &out, &out); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(root, destination))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := scenario.ParseRequest(data)
		if err != nil || len(doc.Tests) != tc.count {
			t.Fatalf("%v: %v", doc, err)
		}
		archive, err := zip.OpenReader(filepath.Join(root, filepath.Dir(destination), "scenario-package.zip"))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range archive.File {
			found = found || f.Name == "fixture.json"
		}
		archive.Close()
		if found != tc.dependency {
			t.Fatal("dependency packaging mismatch")
		}
	}
	for _, args := range [][]string{
		{"--definition-inline-file", "mixed-inline.txt", "--definition-file", "one.feature"},
		{"--definition-file", "scenario.toml", "--definition-file", "one.feature"},
		{"--definition-inline-file", "mixed-toml.txt"},
		{"--definition-file", "multi.yaml"},
		{"--definition-file", "unknown.txt"},
		{"--definition-file", "missing.feature"},
		{"--definition-file", "../outside.feature"},
		{"--definition-file", "bad-dependency.toml"},
		{"--definition-file", "escaping-dependency.toml"},
		{"--definition-file", "one.feature", "--scenario", "scenario.toml"},
	} {
		var out bytes.Buffer
		if err := Run(append([]string{"scenario", "package", "--workspace", root, "--out", "rejected/scenario.toml"}, args...), &out, &out); err == nil {
			t.Fatalf("accepted ambiguous inputs: %v", args)
		}
		if _, err := os.Stat(filepath.Join(root, "rejected")); !os.IsNotExist(err) {
			t.Fatal("invalid definition created a package")
		}
	}
}

func TestInlineTOMLSeparatorRemainsTestContent(t *testing.T) {
	input := "schema='spex.scenario-request/v1'\n[[tests]]\ninline='''Feature: Example\n---\n'''\n"
	doc, tests, err := loadDefinitions("inline", nil, "", func(string) ([]byte, error) { return []byte(input), nil }, true)
	if err != nil || doc == nil || len(tests) != 0 || !strings.Contains(*doc.Tests[0].Inline, "---") {
		t.Fatalf("TOML was split: %v", err)
	}
}
