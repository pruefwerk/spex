package receiver

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

func requestFixture(t *testing.T, author Authoring) (Request, []byte, Expected) {
	t.Helper()
	if author.DefinitionFiles == nil {
		author.DefinitionFiles = []string{}
	}
	e := Envelope{Schema: "spex.transport/v1", RequestID: strings.Repeat("a", 32), Source: Source{Repository: "owner/source", Commit: strings.Repeat("b", 40), Root: ".", RunID: 9}, Authoring: author}
	data, _ := json.Marshal(e)
	sum := sha256.Sum256(data)
	expected := Expected{RequestID: e.RequestID, SHA256: hex.EncodeToString(sum[:]), Repository: e.Source.Repository, Commit: e.Source.Commit, SourceRunID: 9}
	r, err := Decode(data, expected)
	if err != nil {
		t.Fatal(err)
	}
	return r, data, expected
}

type memorySink struct {
	files map[string][]byte
	fail  string
}

func (s *memorySink) Directory() string { return "host-owned" }
func (s *memorySink) Write(name string, data []byte) error {
	if name == s.fail {
		return errors.New("PRIVATE_STORAGE_SENTINEL")
	}
	s.files[name] = append([]byte(nil), data...)
	return nil
}

type policy struct {
	calls   *[]string
	deny    string
	claimed bool
}

func (p *policy) Admit(_ context.Context, _ Envelope) error {
	*p.calls = append(*p.calls, "admit")
	if p.deny == "admit" {
		return errors.New("PRIVATE_POLICY_SENTINEL")
	}
	return nil
}
func (p *policy) Claim(_ context.Context, _ string) error {
	*p.calls = append(*p.calls, "claim")
	if p.claimed {
		return errors.New("duplicate")
	}
	p.claimed = true
	return nil
}
func (p *policy) Allow(_ context.Context, _ scenario.Scenario) error {
	*p.calls = append(*p.calls, "allow")
	if p.deny == "allow" {
		return errors.New("PRIVATE_POLICY_SENTINEL")
	}
	return nil
}

type runtimeFixture struct {
	calls        *[]string
	outcome      scenarioruntime.Outcome
	resolveError bool
	cancel       bool
	tests        int
}

func (r *runtimeFixture) ID() string      { return "fixture/v1" }
func (r *runtimeFixture) Release() string { return "v1.2.3" }
func (r *runtimeFixture) MergeConfig(base, overlay scenario.RawRuntimeConfig) (scenario.RawRuntimeConfig, error) {
	*r.calls = append(*r.calls, "merge")
	return base, nil
}
func (r *runtimeFixture) Resolve(ctx context.Context, request scenarioruntime.ResolveRequest) (scenarioruntime.PreparedRuntime, error) {
	*r.calls = append(*r.calls, "resolve")
	if r.resolveError {
		return nil, errors.New("PRIVATE_RUNTIME_SENTINEL")
	}
	return r, nil
}
func (r *runtimeFixture) Plan(context.Context) (scenarioruntime.ExecutionPlan, error) {
	*r.calls = append(*r.calls, "plan")
	return planFixture{}, nil
}
func (r *runtimeFixture) RedactedDescription() any { return struct{}{} }
func (r *runtimeFixture) Execute(ctx context.Context, _ scenarioruntime.ExecutionPlan, _ scenarioruntime.ArtifactSink) (scenarioruntime.ExecutionResult, error) {
	*r.calls = append(*r.calls, "execute", "cleanup")
	if r.cancel {
		<-ctx.Done()
		return scenarioruntime.ExecutionResult{Outcome: scenarioruntime.Cancelled, Cleanup: "succeeded"}, ctx.Err()
	}
	tests := []scenarioruntime.TestResult{}
	for i := 0; i < r.tests; i++ {
		tests = append(tests, scenarioruntime.TestResult{Name: "fixture", Outcome: r.outcome})
	}
	return scenarioruntime.ExecutionResult{Outcome: r.outcome, Cleanup: "succeeded", Tests: tests}, nil
}

type planFixture struct{}

func (planFixture) Summary() scenarioruntime.PlanSummary {
	return scenarioruntime.PlanSummary{Tests: []scenarioruntime.TestDescription{{Name: "fixture"}}}
}
func hostFixture() (Host, *runtimeFixture, *policy, *memorySink) {
	calls := []string{}
	r := &runtimeFixture{calls: &calls, outcome: scenarioruntime.Passed, tests: 1}
	p := &policy{calls: &calls}
	h := Host{Policy: p, DefaultRuntime: r.ID(), Runtimes: []Choice{{Runtime: r, Default: true}}, Execution: Execution{WorkflowSHA: strings.Repeat("c", 40), RunID: 42, RunAttempt: 1, SpexVersion: "v0.2.0"}}
	return h, r, p, &memorySink{files: map[string][]byte{}}
}
func sourceFixture(t *testing.T, request Request) Checkout {
	t.Helper()
	return Checkout{Directory: t.TempDir(), Repository: request.envelope.Source.Repository, Commit: request.envelope.Source.Commit}
}

func TestDecodeChecksIdentityAndStrictEnvelope(t *testing.T) {
	_, data, expected := requestFixture(t, Authoring{Definition: "Feature: One"})
	for _, input := range [][]byte{append(append([]byte{}, data...), []byte(" {}")...), bytes.Replace(data, []byte(`"authoring":{`), []byte(`"authoring":{"runtime_config":"secret",`), 1), bytes.Replace(data, []byte(`"runtime":""`), []byte(`"runtime":null`), 1), bytes.Replace(data, []byte(`"runtime":""`), []byte(`"runtime":"","runtime":"fixture/v1"`), 1)} {
		sum := sha256.Sum256(input)
		exp := expected
		exp.SHA256 = hex.EncodeToString(sum[:])
		if _, err := Decode(input, exp); err == nil {
			t.Fatal("accepted ambiguous envelope")
		}
	}
	expected.Commit = strings.Repeat("d", 40)
	if _, err := Decode(data, expected); err == nil {
		t.Fatal("wrong source accepted")
	}
}

func TestArtifactDecodeAndRequestImmutability(t *testing.T) {
	r, data, expected := requestFixture(t, Authoring{DefinitionFiles: []string{"a.feature"}})
	copy := r.Envelope()
	copy.Authoring.DefinitionFiles[0] = "changed"
	if r.Envelope().Authoring.DefinitionFiles[0] != "a.feature" {
		t.Fatal("mutable request")
	}
	for _, name := range []string{"request.json", "../request.json"} {
		var buffer bytes.Buffer
		z := zip.NewWriter(&buffer)
		w, _ := z.Create(name)
		_, _ = w.Write(data)
		_ = z.Close()
		_, err := DecodeArtifact(buffer.Bytes(), expected)
		if (err == nil) != (name == "request.json") {
			t.Fatal(err)
		}
	}
}

func TestReceiverLifecycleAndCanonicalEvidence(t *testing.T) {
	request, _, _ := requestFixture(t, Authoring{Definition: "Feature: One\n---\nFeature: Two"})
	h, r, _, sink := hostFixture()
	receipt, err := Execute(context.Background(), request, sourceFixture(t, request), h, sink)
	if err != nil || ExitCode(receipt, err) != 0 {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*r.calls, []string{"admit", "claim", "merge", "allow", "resolve", "plan", "execute", "cleanup"}) {
		t.Fatal(*r.calls)
	}
	doc, err := scenario.Parse(sink.files["scenario.toml"])
	if err != nil || len(doc.Tests) != 2 || doc.RuntimeRelease != "v1.2.3" {
		t.Fatalf("%+v %v", doc, err)
	}
	id, _ := scenario.Identity(doc)
	if receipt.Result.ScenarioID != id || receipt.Result.SpexVersion != "v0.2.0" || receipt.RequestSHA256 != request.SHA256() || receipt.PlannedTests != 1 {
		t.Fatal("receipt lost identity")
	}
	if len(sink.files["receipt.json"]) == 0 {
		t.Fatal("missing receipt")
	}
}

func TestReceiverFailuresDoNotExecute(t *testing.T) {
	for _, mode := range []string{"admit", "allow", "duplicate", "source", "parse", "unsupported", "plan", "missing-policy"} {
		t.Run(mode, func(t *testing.T) {
			author := Authoring{Definition: "Feature: One"}
			if mode == "parse" {
				author.Definition = "invalid"
			}
			if mode == "unsupported" {
				author.Runtime = "other/v1"
			}
			request, _, _ := requestFixture(t, author)
			source := sourceFixture(t, request)
			h, r, p, sink := hostFixture()
			p.deny = mode
			if mode == "duplicate" {
				p.claimed = true
			}
			if mode == "source" {
				source.Commit = "wrong"
			}
			if mode == "plan" {
				r.resolveError = true
			}
			if mode == "missing-policy" {
				h.Policy = nil
			}
			receipt, err := Execute(context.Background(), request, source, h, sink)
			if err == nil || receipt.Result.Outcome != scenarioruntime.Error || strings.Contains(err.Error(), "PRIVATE") || strings.Contains(strings.Join(*r.calls, ","), "execute") {
				t.Fatalf("%v %v", receipt, err)
			}
			if receipt.Result.SpexVersion != "" || len(sink.files["receipt.json"]) == 0 {
				t.Fatal("invalid early failure receipt")
			}
		})
	}
}

func TestReceiverFileDefinitionsAndDependencies(t *testing.T) {
	request, _, _ := requestFixture(t, Authoring{DefinitionFiles: []string{"*.toml"}})
	source := sourceFixture(t, request)
	for name, value := range map[string]string{"scenario.toml": "schema='spex.scenario-request/v1'\ndependencies=['fixture.json']\n[[tests]]\nfile='one.feature'\n", "one.feature": "Feature: One", "fixture.json": "{}"} {
		if err := os.WriteFile(filepath.Join(source.Directory, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h, _, _, sink := hostFixture()
	receipt, err := Execute(context.Background(), request, source, h, sink)
	if err != nil || receipt.Result.Outcome != scenarioruntime.Passed {
		t.Fatal(err)
	}
}

func TestReceiverCancellationAndReportingFailures(t *testing.T) {
	for _, mode := range []string{"cancel", "test-failure", "incomplete", "report"} {
		author := Authoring{Definition: "Feature: One", Timeout: "1ms"}
		request, _, _ := requestFixture(t, author)
		h, r, _, sink := hostFixture()
		if mode == "cancel" {
			r.cancel = true
		}
		if mode == "test-failure" {
			r.outcome = scenarioruntime.Failed
			sink.fail = "receipt.json"
		}
		if mode == "incomplete" {
			r.tests = 0
		}
		if mode == "report" {
			sink.fail = "receipt.json"
		}
		receipt, err := Execute(context.Background(), request, sourceFixture(t, request), h, sink)
		if err == nil || receipt.Result.Outcome == scenarioruntime.Passed {
			t.Fatalf("%s: %v %v", mode, receipt, err)
		}
		if mode == "test-failure" && ExitCode(receipt, err) != 3 {
			t.Fatal("lost primary failure")
		}
		if mode == "cancel" && ExitCode(receipt, err) != 130 {
			t.Fatal("lost cancellation")
		}
	}
}
