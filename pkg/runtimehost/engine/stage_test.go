package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageRuntimeIsPrivateRepeatableAndDoesNotMutateAction(t *testing.T) {
	root, file := configuredFixture(t)
	parent := t.TempDir()
	output := filepath.Join(parent, "outputs")
	for path, value := range map[string]string{"scripts/fixture.sh": "#!/bin/sh\n", ".secrets/key": "PRIVATE_SENTINEL", ".spex/context.json": "PRIVATE_SENTINEL"} {
		target := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(value), 0700); err != nil {
			t.Fatal(err)
		}
	}
	var previous string
	for range 2 {
		if err := os.WriteFile(output, nil, 0600); err != nil {
			t.Fatal(err)
		}
		env := func(key string) string {
			if key == "RUNNER_TEMP" {
				return parent
			}
			if key == "GITHUB_OUTPUT" {
				return output
			}
			return ""
		}
		var logs bytes.Buffer
		if err := stageRuntime(context.Background(), []string{"--source", root, "--config", filepath.Base(file)}, env, &logs); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(output)
		values := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			key, value, _ := strings.Cut(line, "=")
			values[key] = value
		}
		destination := values["runtime-directory"]
		if previous != "" && values["runtime-release"] != previous {
			t.Fatal("unstable bundle identity")
		}
		previous = values["runtime-release"]
		if destination == root || !strings.HasPrefix(destination, parent+string(filepath.Separator)) {
			t.Fatal("action reused as runtime")
		}
		for _, path := range []string{".secrets/key", ".spex/context.json"} {
			if _, err := os.Stat(filepath.Join(destination, path)); !os.IsNotExist(err) {
				t.Fatal("private source data staged")
			}
		}
		if _, err := os.Stat(filepath.Join(destination, "scripts/fixture.sh")); err != nil {
			t.Fatal(err)
		}
		h, err := New(values["runtime-config"], "test")
		if err != nil {
			t.Fatal(err)
		}
		if h.config.Namespace != "acceptance" {
			t.Fatal("selected configuration lost")
		}
		if strings.Contains(logs.String(), "PRIVATE_") {
			t.Fatal("secret leaked")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "spex-runtime.toml")); !os.IsNotExist(err) {
		t.Fatal("trusted source mutated")
	}
}

func TestStageRejectsUntrustedPathsAndDoesNotLeavePartialWorkspace(t *testing.T) {
	root, file := configuredFixture(t)
	parent := t.TempDir()
	env := func(key string) string {
		if key == "RUNNER_TEMP" {
			return parent
		}
		return ""
	}
	if err := stageRuntime(context.Background(), []string{"--source", root, "--config", "../host.toml"}, env, &bytes.Buffer{}); err == nil {
		t.Fatal("escaping definition accepted")
	}
	if err := os.Symlink(file, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := stageRuntime(context.Background(), []string{"--source", root, "--config", filepath.Base(file)}, env, &bytes.Buffer{}); err == nil {
		t.Fatal("symlink accepted")
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 0 {
		t.Fatal("partial workspace retained")
	}
}

func TestStageCancellationCreatesNoWorkspace(t *testing.T) {
	root, file := configuredFixture(t)
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := stageRuntime(ctx, []string{"--source", root, "--config", filepath.Base(file)}, func(key string) string {
		if key == "RUNNER_TEMP" {
			return parent
		}
		return ""
	}, &bytes.Buffer{}); err == nil {
		t.Fatal("cancelled stage proceeded")
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 0 {
		t.Fatal("cancelled stage created workspace")
	}
}

func TestStageCLIRequiresNoDefinitionInCallerCheckout(t *testing.T) {
	root, file := configuredFixture(t)
	t.Chdir(t.TempDir())
	t.Setenv("RUNNER_TEMP", t.TempDir())
	var out, errs bytes.Buffer
	if Run(context.Background(), []string{"stage", "--source", root, "--config", filepath.Base(file)}, &out, &errs, "test") != 0 {
		t.Fatal(errs.String())
	}
}
