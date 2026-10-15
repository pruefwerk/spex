package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReceiverPropagatesOwnedSuiteToProfilePreflight(t *testing.T) {
	for _, mode := range []string{"", "reusable"} {
		for _, inheritedSuite := range []string{"", "unrelated-suite.yaml"} {
			t.Run(mode+"/"+inheritedSuite, func(t *testing.T) {
				root, file := configuredFixture(t)
				t.Chdir(root)
				h, err := New(file, "test")
				if err != nil {
					t.Fatal(err)
				}
				paths, err := h.prepareExecution(root, h.config.Suite, func(string) string { return "" })
				if err != nil {
					t.Fatal(err)
				}
				name := filepath.Base(filepath.Dir(paths.Kubeconfig))
				id := strings.Repeat("a", 64)
				if err := os.WriteFile(filepath.Join(filepath.Dir(paths.Kubeconfig), "nodes.json"), []byte(fmt.Sprintf("[%q]", id)), 0600); err != nil {
					t.Fatal(err)
				}
				bin := t.TempDir()
				docker := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\nps) echo %s;;\ninspect) echo '[{\"Id\":\"%s\",\"Config\":{\"Labels\":{\"io.x-k8s.kind.cluster\":\"%s\"}}}]';;\n*) exit 99;;\nesac\n", id, id, name)
				if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(docker), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv(h.config.ScopeEnvironment, "unrelated-scope")
				t.Setenv("KUBECONFIG", "unrelated-kubeconfig")
				t.Setenv("SPEX_SUITE", inheritedSuite)
				private := filepath.Join(root, ".spex", "receiver-private")
				if err := os.MkdirAll(private, 0700); err != nil {
					t.Fatal(err)
				}
				admitted := admissionContext{
					Schema: h.resolver.Protocol("receiver-admission"), Root: root,
					Scope: paths.Scope, Suite: paths.Suite, Kubeconfig: paths.Kubeconfig,
					Artifact: filepath.Join(private, "request.zip"), Actor: "developer",
					Mode: mode, Release: "bundle-" + strings.Repeat("a", 64),
				}
				admitted.Checkout.Directory = filepath.Join(private, "source")
				data, err := json.Marshal(admitted)
				if err != nil {
					t.Fatal(err)
				}
				contextPath := filepath.Join(private, "context.json")
				if err := os.WriteFile(contextPath, data, 0600); err != nil {
					t.Fatal(err)
				}
				// Stop at the absent request archive, after the real receiver entry
				// point installs its environment, without starting a cluster.
				var output bytes.Buffer
				if code := h.run(context.Background(), contextPath, &output, &output); code != 1 {
					t.Fatalf("missing request archive: code=%d; %s", code, output.String())
				}
				for key, want := range map[string]string{h.config.ScopeEnvironment: paths.Scope, "SPEX_SUITE": paths.Suite, "KUBECONFIG": paths.Kubeconfig} {
					if os.Getenv(key) != want {
						t.Fatalf("receiver did not propagate %s", key)
					}
				}
				// KUTTL replaces KUBECONFIG, but must inherit the admitted suite.
				t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "kubeconfig"))
				if code := h.Run(context.Background(), []string{"execution", "profile", "--root", root, "--context", "kind-" + name}, &output, &output); code != 0 {
					t.Fatalf("receiver profile preflight: code=%d; %s", code, output.String())
				}
			})
		}
	}
}

func TestProfilePreflightAcceptsKUTTLKubeconfigWithoutWeakeningOwnership(t *testing.T) {
	root, file := configuredFixture(t)
	t.Chdir(root)
	h, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := h.prepareExecution(root, h.config.Suite, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(filepath.Dir(paths.Kubeconfig))
	id := strings.Repeat("a", 64)
	if err := os.WriteFile(filepath.Join(filepath.Dir(paths.Kubeconfig), "nodes.json"), []byte(fmt.Sprintf("[%q]", id)), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	docker := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\nps) echo %s;;\ninspect) echo '[{\"Id\":\"%s\",\"Config\":{\"Labels\":{\"io.x-k8s.kind.cluster\":\"%s\"}}}]';;\n*) exit 99;;\nesac\n", id, id, name)
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(docker), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(h.config.ScopeEnvironment, paths.Scope)
	t.Setenv("SPEX_SUITE", paths.Suite)
	// This is the override installed by KUTTL's RunCommand, not a new scope.
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "kubeconfig"))
	for _, tt := range []struct {
		name  string
		extra []string
		want  int
	}{
		{"KUTTL workspace copy", []string{"--context", "kind-" + name}, 0},
		{"wrong context", []string{"--context", "kind-other"}, 1},
		{"explicit foreign kubeconfig", []string{"--context", "kind-" + name, "--kubeconfig", "foreign"}, 1},
		{"foreign suite", []string{"--context", "kind-" + name, "--suite", "other.yaml"}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			args := append([]string{"--config", file, "execution", "profile", "--root", root}, tt.extra...)
			if code := Run(context.Background(), args, &out, &out, "test"); code != tt.want {
				t.Fatalf("code=%d want=%d; %s", code, tt.want, out.String())
			}
		})
	}
	var checkOutput bytes.Buffer
	if code := Run(context.Background(), []string{"--config", file, "execution", "check"}, &checkOutput, &checkOutput, "test"); code == 0 {
		t.Fatal("ordinary execution check accepted a foreign kubeconfig path")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(paths.Kubeconfig), "nodes.json"), []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := Run(context.Background(), []string{"--config", file, "execution", "profile", "--context", "kind-" + name}, &out, &out, "test"); code == 0 {
		t.Fatal("unrecorded live node admitted")
	}
}
