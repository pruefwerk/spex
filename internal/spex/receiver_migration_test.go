package spex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/pkg/receiver"
	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

// Fixture-only admission. Production hosts must authenticate and persist claims.
type fixtureReceiverPolicy struct{}

func TestReceiverMigrationRuntimeRequiresRelease(t *testing.T) {
	if _, err := NewReceiverMigrationRuntime(repoRoot(t), "examples/suites/mqtt-local.yaml", ""); err == nil {
		t.Fatal("accepted a receiver runtime without a release pin")
	}
}

func (fixtureReceiverPolicy) Admit(context.Context, receiver.Envelope) error { return nil }
func (fixtureReceiverPolicy) Claim(context.Context, string) error            { return nil }
func (fixtureReceiverPolicy) Allow(context.Context, scenario.Scenario) error { return nil }

func TestReceiverUsesExistingMigrationRuntime(t *testing.T) {
	root := repoRoot(t)
	runtime, err := NewReceiverMigrationRuntime(root, "examples/suites/mqtt-local.yaml", "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	bin := t.TempDir()
	fake, err := os.ReadFile(writeFakeKubectl(t, 0, "PRIVATE_BACKEND_SENTINEL"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), fake, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e := receiver.Envelope{Schema: "spex.transport/v1", RequestID: strings.Repeat("a", 32), Source: receiver.Source{Repository: "owner/source", Commit: strings.Repeat("b", 40), Root: ".", RunID: 9}, Authoring: receiver.Authoring{Definition: scenarioSmokeSource, DefinitionFiles: []string{}}}
	data, _ := json.Marshal(e)
	sum := sha256.Sum256(data)
	request, err := receiver.Decode(data, receiver.Expected{RequestID: e.RequestID, SHA256: hex.EncodeToString(sum[:]), Repository: e.Source.Repository, Commit: e.Source.Commit, SourceRunID: 9})
	if err != nil {
		t.Fatal(err)
	}
	sink, err := scenarioruntime.NewFileArtifacts(t.TempDir(), "runs", request.SHA256())
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	host := receiver.Host{Policy: fixtureReceiverPolicy{}, DefaultRuntime: runtime.ID(), Runtimes: []receiver.Choice{{Runtime: runtime, Default: true}}, Execution: receiver.Execution{WorkflowSHA: strings.Repeat("c", 40), RunID: 42, RunAttempt: 1, SpexVersion: Version}}
	receipt, err := receiver.Execute(context.Background(), request, receiver.Checkout{Directory: source, Repository: e.Source.Repository, Commit: e.Source.Commit}, host, sink)
	if err != nil || receipt.Result.Outcome != scenarioruntime.Passed || receipt.PlannedTests != 1 {
		t.Fatalf("%+v %v", receipt, err)
	}
	for _, name := range []string{"receipt.json", "result.json", "scenario.toml"} {
		data, err := os.ReadFile(filepath.Join(sink.Directory(), name))
		if err != nil || strings.Contains(string(data), "PRIVATE_BACKEND_SENTINEL") {
			t.Fatalf("invalid evidence %s: %v", name, err)
		}
	}
}
