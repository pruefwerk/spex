package spex

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestSuiteTagCombination(t *testing.T) {
	tags := []string{"a", "b"}
	if !matchesSuiteAnyTags(tags, nil) || !matchesSuiteAnyTags(tags, []string{}) {
		t.Fatal("empty OR filter must not restrict selection")
	}
	if !matchesSuiteAnyTags(tags, []string{"absent", "b"}) || matchesSuiteAnyTags(tags, []string{"absent"}) {
		t.Fatal("OR filter must match any requested tag")
	}
	if matchesSuiteTagFilters(tags, []string{"a", "absent"}, nil) {
		t.Fatal("existing include filter must retain AND semantics")
	}
	if matchesSuiteTagFilters(tags, nil, []string{"b"}) {
		t.Fatal("exclusion must take precedence")
	}
}

func TestSuiteListAnyTag(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	suite := filepath.Join(root, "examples", "suites", "mqtt-local.yaml")
	list := func(extra ...string) ([]struct {
		Tags []string `json:"tags"`
	}, error) {
		var stdout, stderr bytes.Buffer
		args := append([]string{"suite", "list", "--suite", suite, "--format", "json"}, extra...)
		err := Run(args, &stdout, &stderr)
		var result struct {
			Scenarios []struct {
				Tags []string `json:"tags"`
			} `json:"scenarios"`
		}
		if err == nil {
			if err = json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result.Scenarios, err
	}
	all, err := list("--include-any-tag", "")
	if err != nil || len(all) != 5 {
		t.Fatalf("empty filter: %d scenarios, %v", len(all), err)
	}
	want := 0
	for _, scenario := range all {
		if matchesSuiteAnyTags(scenario.Tags, []string{"regression"}) {
			want++
		}
	}
	if want == 0 || want == len(all) {
		t.Fatal("fixture must exercise a proper subset")
	}
	selected, err := list("--include-any-tag", "missing,regression", "--include-any-tag", "regression")
	if err != nil || len(selected) != want {
		t.Fatalf("OR selection: %d, want %d, %v", len(selected), want, err)
	}
	if _, err := list("--include-any-tag", "missing"); err == nil {
		t.Fatal("empty selection must fail")
	}
	if _, err := list("--include-any-tag", "regression", "--exclude-tag", "regression"); err == nil {
		t.Fatal("excluded selection must fail")
	}
}
