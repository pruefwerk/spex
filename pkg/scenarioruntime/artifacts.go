package scenarioruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/pruefwerk/spex/pkg/scenario"
)

// FileArtifacts confines writes to a new execution directory. Scenario identity
// remains semantic; the random execution suffix only keeps reruns separate.
type FileArtifacts struct {
	root      *os.Root
	directory string
}

func NewFileArtifacts(workspace, base, id string) (*FileArtifacts, error) {
	if len(id) != 64 {
		return nil, errors.New("invalid scenario identity")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return nil, errors.New("invalid scenario identity")
	}
	base, err := scenario.RelativePath(base)
	if err != nil {
		return nil, errors.New("artifact directory must remain within workspace")
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, errors.New("artifact workspace unavailable")
	}
	defer root.Close()
	parent := filepath.Join(base, id)
	if err := root.MkdirAll(parent, 0o700); err != nil {
		return nil, errors.New("cannot create artifact directory")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, errors.New("cannot allocate execution identity")
	}
	relative := filepath.Join(parent, hex.EncodeToString(nonce[:]))
	if err := root.Mkdir(relative, 0o700); err != nil {
		return nil, errors.New("cannot create execution directory")
	}
	execution, err := root.OpenRoot(relative)
	if err != nil {
		return nil, errors.New("cannot open execution directory")
	}
	return &FileArtifacts{root: execution, directory: filepath.Join(workspace, relative)}, nil
}

func (a *FileArtifacts) Directory() string { return a.directory }
func (a *FileArtifacts) Close() error      { return a.root.Close() }
func (a *FileArtifacts) Write(name string, data []byte) error {
	name, err := scenario.RelativePath(name)
	if err != nil {
		return errors.New("invalid artifact name")
	}
	if err := a.root.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return errors.New("cannot create artifact parent")
	}
	file, err := a.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("artifact exists or cannot be created")
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = a.root.Remove(name)
		return errors.New("artifact write failed")
	}
	return nil
}

// Run writes only the canonical document, safe plan/description, and structured
// result. Concrete runtimes must project their evidence before writing it here.
func Run(ctx context.Context, p Prepared, sink ArtifactSink) (ExecutionResult, error) {
	if sink == nil || p.Runtime == nil || p.Plan == nil {
		return ExecutionResult{}, errors.New("prepared runtime and artifact sink required")
	}
	canonical, err := scenario.SerializeCanonical(p.Scenario)
	if err != nil {
		return ExecutionResult{}, err
	}
	plan, err := json.Marshal(p.Plan.Summary())
	if err != nil {
		return ExecutionResult{}, errors.New("cannot serialize plan")
	}
	runtime, err := json.Marshal(p.Runtime.RedactedDescription())
	if err != nil {
		return ExecutionResult{}, errors.New("cannot serialize runtime description")
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{"scenario.toml", canonical}, {"plan.json", plan}, {"runtime.json", runtime}} {
		if err := sink.Write(file.name, file.data); err != nil {
			id, _ := scenario.Identity(p.Scenario)
			result := ExecutionResult{Schema: "spex.result/v1", ScenarioID: id, Runtime: p.Scenario.Runtime, Outcome: Error, Cleanup: "not_run", Tests: []TestResult{}, Artifacts: []string{}, Problems: []Problem{{Phase: "preparation", Code: "artifact_preparation_failed", Message: "Execution description could not be persisted; execution did not start"}}}
			data, _ := json.Marshal(result)
			_ = sink.Write("result.json", data)
			return result, errors.New("cannot persist execution description; execution did not start")
		}
	}
	result, primary := p.Execute(ctx, sink)
	result.Artifacts = append(result.Artifacts, "scenario.toml", "plan.json", "runtime.json", "result.json")
	data, err := json.MarshalIndent(result, "", "  ")
	if err == nil {
		err = sink.Write("result.json", data)
	}
	if err != nil {
		result.Problems = append(result.Problems, Problem{Phase: "reporting", Code: "result_write_failed", Message: "Execution result could not be persisted"})
		if result.Outcome == Passed {
			result.Outcome = Error
		}
		if primary != nil {
			return result, primary
		}
		return result, errors.New("execution result could not be persisted")
	}
	return result, primary
}
