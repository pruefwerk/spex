package spex

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

func TestRemotePackageNeedsNoRuntimeWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "test.feature"), []byte("Feature: Smoke\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var previous remoteRequest
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		err := Run([]string{"scenario", "package", "--workspace", root, "--runtime", "migration-testbench/v1", "--spex-file", "test.feature", "--out", fmt.Sprintf("out%d/scenario.toml", i)}, &out, &out)
		if err != nil {
			t.Fatal(err)
		}
		var request remoteRequest
		if json.Unmarshal(out.Bytes(), &request) != nil {
			t.Fatal("invalid package summary")
		}
		if i == 1 && (request.ScenarioID != previous.ScenarioID || request.PackageSHA != previous.PackageSHA || request.RequestID == previous.RequestID) {
			t.Fatal("semantic identity or request identity incorrect")
		}
		archive, err := zip.OpenReader(filepath.Join(root, request.PackagePath))
		if err != nil {
			t.Fatal(err)
		}
		if len(archive.File) != 2 || archive.File[0].Name != "scenario.toml" || archive.File[1].Name != "test.feature" {
			t.Fatal("unexpected package contents")
		}
		archive.Close()
		previous = request
	}
}

func TestRemotePackageRejectsEscapingDependencies(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret", "outside/secret", "scenario.toml"} {
		var out bytes.Buffer
		err := Run([]string{"scenario", "package", "--workspace", root, "--runtime", "migration-testbench/v1", "--support-file", path, "--out", "out/scenario.toml"}, &out, &out)
		if err == nil {
			t.Fatal("accepted unsafe source")
		}
	}
}

func TestRemoteSubmissionProtocol(t *testing.T) {
	for _, mode := range []string{"passed", "failed", "missing", "mismatch", "wrong-commit", "duplicate", "timeout", "dispatch-error", "workflow-failure", "secret-problem", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			sha := strings.Repeat("c", 40)
			request := remoteRequest{Schema: "spex.submission/v1", RequestID: strings.Repeat("a", 32), ScenarioID: strings.Repeat("b", 64), PackageSHA: strings.Repeat("d", 64), Runtime: "migration-testbench/v1", RequestPath: "submission/request.json"}
			posts, cancels := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer SENTINEL_TOKEN" {
					t.Error("missing authorization")
				}
				switch {
				case strings.Contains(r.URL.Path, "/commits/"):
					json.NewEncoder(w).Encode(map[string]string{"sha": sha})
				case r.URL.Path == "/repos/caller/repo/actions/artifacts/11":
					fmt.Fprintf(w, `{"name":"spex-request-%s","workflow_run":{"id":9}}`, request.RequestID)
				case strings.HasSuffix(r.URL.Path, "/dispatches"):
					posts++
					if mode == "dispatch-error" {
						w.WriteHeader(500)
						fmt.Fprint(w, "SENTINEL_TOKEN")
						return
					}
					var body struct {
						Ref    string            `json:"ref"`
						Inputs map[string]string `json:"inputs"`
					}
					json.NewDecoder(r.Body).Decode(&body)
					if body.Ref != "v1" || body.Inputs["request_id"] != request.RequestID || body.Inputs["package_sha256"] != request.PackageSHA {
						t.Error("dispatch contract mismatch")
					}
					w.WriteHeader(204)
				case strings.HasSuffix(r.URL.Path, "/cancel"):
					cancels++
					w.WriteHeader(202)
				case strings.HasSuffix(r.URL.Path, "/runs"):
					run := remoteRun{ID: 42, Title: "spex:" + request.RequestID, SHA: sha, Status: "completed", Conclusion: "success", Attempt: 1}
					if mode == "wrong-commit" {
						run.SHA = strings.Repeat("e", 40)
					}
					if mode == "timeout" {
						run.Status = "in_progress"
					}
					if mode == "workflow-failure" {
						run.Conclusion = "failure"
					}
					runs := []remoteRun{run}
					if mode == "duplicate" {
						runs = append(runs, run)
					}
					json.NewEncoder(w).Encode(map[string]any{"workflow_runs": runs})
				case strings.HasSuffix(r.URL.Path, "/artifacts"):
					if mode == "missing" {
						fmt.Fprint(w, `{"artifacts":[]}`)
						return
					}
					fmt.Fprintf(w, `{"artifacts":[{"id":12,"name":"spex-result-%s"}]}`, request.RequestID)
				case strings.HasSuffix(r.URL.Path, "/12/zip"):
					result := scenarioruntime.ExecutionResult{Schema: "spex.result/v1", ScenarioID: request.ScenarioID, Runtime: request.Runtime, Outcome: scenarioruntime.Passed, Cleanup: "succeeded", Tests: []scenarioruntime.TestResult{{Name: "SENTINEL_PRIVATE", Outcome: scenarioruntime.Passed}}}
					if mode == "failed" {
						result.Outcome = scenarioruntime.Failed
						result.Tests[0].Outcome = scenarioruntime.Failed
					}
					if mode == "secret-problem" {
						result.Problems = []scenarioruntime.Problem{{Message: "SENTINEL_PRIVATE"}}
					}
					receipt := remoteReceipt{Schema: "spex.remote-result/v1", RequestID: request.RequestID, ScenarioID: request.ScenarioID, PackageSHA: request.PackageSHA, RuntimeSHA: sha, RunID: 42, RunAttempt: 1, PlannedTests: 1, Result: result}
					if mode == "incomplete" {
						receipt.PlannedTests = 2
					}
					if mode == "mismatch" {
						receipt.RequestID = "wrong"
					}
					var b bytes.Buffer
					z := zip.NewWriter(&b)
					f, _ := z.Create("receipt.json")
					json.NewEncoder(f).Encode(receipt)
					z.Close()
					w.Write(b.Bytes())
				default:
					t.Errorf("unexpected API path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			g := githubRemote{base: server.URL, token: "SENTINEL_TOKEN", client: server.Client(), poll: time.Millisecond}
			var out bytes.Buffer
			wait := time.Second
			if mode == "timeout" {
				wait = 20 * time.Millisecond
			}
			err := submitRemote(context.Background(), g, request, root, "runtime/repo", "acceptance.yaml", "v1", "caller/repo", 9, 11, wait, &out)
			if (err == nil) != (mode == "passed") {
				t.Fatalf("unexpected result: %v", err)
			}
			if posts != 1 {
				t.Fatal("dispatch retried")
			}
			if mode == "timeout" && (ExitCode(err) != 130 || cancels != 1) {
				t.Fatal("cancellation was not propagated")
			}
			if mode == "failed" && ExitCode(err) != 3 {
				t.Fatal("test failure classification lost")
			}
			if mode == "incomplete" || mode == "workflow-failure" {
				data, err := os.ReadFile(filepath.Join(root, "submission/result.json"))
				var result scenarioruntime.ExecutionResult
				if err != nil || json.Unmarshal(data, &result) != nil || result.Outcome != scenarioruntime.Error {
					t.Fatal("persisted misleading success")
				}
			}
			filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() {
					data, _ := os.ReadFile(path)
					if bytes.Contains(data, []byte("SENTINEL")) {
						t.Error("secret persisted")
					}
				}
				return nil
			})
			if strings.Contains(out.String(), "SENTINEL") || (err != nil && strings.Contains(err.Error(), "SENTINEL")) {
				t.Fatal("unsafe diagnostics")
			}
			// The exclusive submission record blocks a blind retry before POST.
			if err := submitRemote(context.Background(), g, request, root, "runtime/repo", "acceptance.yaml", "v1", "caller/repo", 9, 11, time.Second, &out); err == nil || posts != 1 {
				t.Fatal("duplicate submission accepted")
			}
		})
	}
}

func TestRemotePackageInline(t *testing.T) {
	inline := "Feature: Example\n"
	doc := scenario.Scenario{Schema: scenario.Schema, Runtime: "migration-testbench/v1", Tests: []scenario.TestSource{{Inline: &inline}}}
	var out bytes.Buffer
	if err := packageScenario(doc, t.TempDir(), "out/scenario.toml", nil, &out); err != nil {
		t.Fatal(err)
	}
}
