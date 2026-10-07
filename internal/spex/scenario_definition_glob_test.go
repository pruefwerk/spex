package spex

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefinitionGlobs(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"acceptance/a.feature", "acceptance/deep/b.feature", "acceptance/deep/c.yaml", ".git/hidden.feature"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("Feature: Fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ patterns, want []string }{
		{[]string{"acceptance/**/*.feature"}, []string{"acceptance/a.feature", "acceptance/deep/b.feature"}},
		{[]string{"acceptance/*.feature"}, []string{"acceptance/a.feature"}},
		{[]string{"acceptance/**/*.feature", "acceptance/a.feature", "./acceptance/a.feature"}, []string{"acceptance/a.feature", "acceptance/deep/b.feature"}},
		{[]string{"acceptance/**/[ab].feature"}, []string{"acceptance/a.feature", "acceptance/deep/b.feature"}},
		{[]string{"acceptance/deep/?.yaml", "acceptance/a.feature"}, []string{"acceptance/a.feature", "acceptance/deep/c.yaml"}},
	} {
		got, err := expandDefinitionFiles(root, tc.patterns)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%v: got %v, %v", tc.patterns, got, err)
		}
	}
	for _, pattern := range []string{"missing/*.feature", "../*.feature", "/absolute/*", "acceptance/[bad", "acceptance/**suffix", ".git/*.feature"} {
		if _, err := expandDefinitionFiles(root, []string{pattern}); err == nil {
			t.Fatalf("accepted %q", pattern)
		}
	}
	if _, err := expandDefinitionFiles(root, []string{"acceptance/*.feature", "missing/*.feature"}); err == nil {
		t.Fatal("unmatched pattern silently ignored")
	}
	outside := filepath.Join(t.TempDir(), "outside.feature")
	if err := os.WriteFile(outside, []byte("Feature: Outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside.feature")); err != nil {
		t.Fatal(err)
	}
	if _, err := expandDefinitionFiles(root, []string{"*.feature"}); err == nil {
		t.Fatal("outside symlink accepted")
	}
}

func TestOverlappingDefinitionGlobsBecomeOneTest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "one.feature"), []byte("Feature: One"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, tests, err := loadDefinitions("", []string{"*.feature", "one.feature"}, root, os.ReadFile, true)
	if err != nil || len(tests) != 1 || *tests[0].File != "one.feature" {
		t.Fatalf("tests=%v err=%v", tests, err)
	}
}

func TestDefinitionGlobLimitsAndGitMetadataFile(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one.feature", ".git"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := expandDefinitionFiles(root, []string{"**"})
	if err != nil || !reflect.DeepEqual(files, []string{"one.feature"}) {
		t.Fatalf("git metadata included: %v %v", files, err)
	}
	for _, patterns := range [][]string{make([]string, 1001), {strings.Repeat("a", 4097)}, {strings.Repeat("**/", 257) + "one.feature"}} {
		if _, err := expandDefinitionFiles(root, patterns); err == nil {
			t.Fatal("oversized glob input accepted")
		}
	}
}
