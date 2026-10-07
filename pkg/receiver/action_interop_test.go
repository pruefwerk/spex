package receiver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

// This optional cross-repository contract gate invokes the real Action transport
// with no network. Execution uses the fixture runtime, not live acceptance.
func TestActionTransportRoundTrip(t *testing.T) {
	action := os.Getenv("SPEX_ACTION_SOURCE")
	if action == "" {
		t.Skip("set SPEX_ACTION_SOURCE to a reviewed spex-action checkout")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	prepare := `import contextlib, importlib.util, io, json, os, sys
spec = importlib.util.spec_from_file_location("transport", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
with contextlib.redirect_stdout(io.StringIO()):
    outputs = module.prepare(os.environ)
print(json.dumps(outputs))
`
	command := exec.Command(python, "-c", prepare, filepath.Join(action, "scripts", "transport.py"))
	command.Dir = root
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_WORKSPACE=" + root, "RUNNER_TEMP=" + root, "GITHUB_REPOSITORY=owner/source", "GITHUB_SHA=" + strings.Repeat("b", 40), "GITHUB_RUN_ID=9", "SPEX_ACTION_RUNTIME_WORKFLOW=owner/runtime/.github/workflows/acceptance.yaml@v1", "SPEX_ACTION_DEFINITION=Feature: Fixture"}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("action prepare failed: %v", err)
	}
	var prepared map[string]string
	if err := json.Unmarshal(output, &prepared); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(prepared["request-path"])
	if err != nil {
		t.Fatal(err)
	}
	request, err := Decode(data, Expected{RequestID: prepared["request-id"], SHA256: prepared["request-sha256"], Repository: "owner/source", Commit: strings.Repeat("b", 40), SourceRunID: 9})
	if err != nil {
		t.Fatal(err)
	}
	h, _, _, _ := hostFixture()
	sink, err := scenarioruntime.NewFileArtifacts(t.TempDir(), "runs", request.SHA256())
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	receipt, err := Execute(context.Background(), request, Checkout{Directory: root, Repository: "owner/source", Commit: strings.Repeat("b", 40)}, h, sink)
	if err != nil {
		t.Fatal(err)
	}
	validate := `import importlib.util, io, json, pathlib, sys, zipfile
spec = importlib.util.spec_from_file_location("transport", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
request = json.loads(pathlib.Path(sys.argv[2]).read_text())
receipt = pathlib.Path(sys.argv[3]).read_bytes()
class API:
    def call(self, method, path, body=None, timeout=30, binary=False):
        if binary:
            stream = io.BytesIO()
            with zipfile.ZipFile(stream, "w") as archive:
                archive.writestr("receipt.json", receipt)
            return stream.getvalue()
        return {"artifacts": [{"id": 456, "name": "spex-result-" + request["request_id"], "expired": False}]}
result = module.receipt_result(API(), "owner/runtime", {"id": 42, "conclusion": "success"}, request, sys.argv[4], "c" * 40, lambda: 30)
assert result["outcome"] == "passed"
assert result["scenario_id"] == sys.argv[5]
print("action-receiver-action contract passed")
`
	command = exec.Command(python, "-c", validate, filepath.Join(action, "scripts", "transport.py"), prepared["request-path"], filepath.Join(sink.Directory(), "receipt.json"), request.SHA256(), receipt.Result.ScenarioID)
	if _, err := command.CombinedOutput(); err != nil {
		t.Fatalf("action rejected receiver receipt: %v", err)
	}
}
