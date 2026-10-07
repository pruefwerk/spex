package spex

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var hex32Pattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hex64Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var shaPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var workflowPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+\.ya?ml$`)

type githubRemote struct {
	base, token string
	client      *http.Client
	poll        time.Duration
}
type remoteRun struct {
	ID         int64  `json:"id"`
	Title      string `json:"display_title"`
	SHA        string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Attempt    int    `json:"run_attempt"`
}
type remoteReceipt struct {
	Schema             string                          `json:"schema"`
	RequestID          string                          `json:"request_id"`
	ScenarioID         string                          `json:"scenario_id"`
	PackageSHA         string                          `json:"package_sha256"`
	RuntimeSHA         string                          `json:"runtime_sha"`
	RunID              int64                           `json:"run_id"`
	RunAttempt         int                             `json:"run_attempt"`
	PlannedTests       int                             `json:"planned_tests"`
	ResolvedScenarioID string                          `json:"resolved_scenario_id,omitempty"`
	RuntimeRelease     string                          `json:"runtime_release,omitempty"`
	Result             scenarioruntime.ExecutionResult `json:"result"`
}

func (g githubRemote) call(ctx context.Context, method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, errors.New("cannot encode remote request")
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, reader)
	if err != nil {
		return nil, errors.New("invalid remote request")
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := g.client.Do(req)
	if err != nil {
		return nil, errors.New("GitHub request failed; remote acceptance may be uncertain")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub request failed (HTTP %d)", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (32<<20)+1))
	if err != nil || len(data) > 32<<20 {
		return nil, errors.New("remote response unavailable or too large")
	}
	return data, nil
}

func (g githubRemote) lookup(ctx context.Context, repo, workflow, request, sha string) (remoteRun, error) {
	var found remoteRun
	for page := 1; page <= 10; page++ {
		data, err := g.call(ctx, "GET", "/repos/"+repo+"/actions/workflows/"+workflow+"/runs?event=workflow_dispatch&per_page=100&page="+strconv.Itoa(page), nil)
		if err != nil {
			return found, err
		}
		var list struct {
			Runs []remoteRun `json:"workflow_runs"`
		}
		if json.Unmarshal(data, &list) != nil {
			return found, errors.New("invalid workflow run response")
		}
		for _, run := range list.Runs {
			if run.Title != "spex:"+request {
				continue
			}
			if run.SHA != sha || run.Attempt != 1 || run.ID <= 0 || found.ID != 0 {
				return remoteRun{}, errors.New("ambiguous or unexpected remote execution")
			}
			found = run
		}
		if len(list.Runs) < 100 {
			return found, nil
		}
	}
	return remoteRun{}, errors.New("workflow history exceeds correlation limit")
}

// Remote results are envelopes, not arbitrary archives extracted onto the caller.
// Preserve only the verified receipt and structured result. Receiver evidence
// remains a separately inspectable artifact in the remote workflow.
func (g githubRemote) result(ctx context.Context, repo string, run remoteRun, request remoteRequest, sha string) (remoteReceipt, error) {
	var receipt remoteReceipt
	var artifactID int64
	for page := 1; page <= 10; page++ {
		data, err := g.call(ctx, "GET", fmt.Sprintf("/repos/%s/actions/runs/%d/artifacts?per_page=100&page=%d", repo, run.ID, page), nil)
		if err != nil {
			return receipt, err
		}
		var list struct {
			Artifacts []struct {
				ID      int64  `json:"id"`
				Name    string `json:"name"`
				Expired bool   `json:"expired"`
			} `json:"artifacts"`
		}
		if json.Unmarshal(data, &list) != nil {
			return receipt, errors.New("invalid result artifact response")
		}
		for _, a := range list.Artifacts {
			if a.Name == "spex-result-"+request.RequestID {
				if artifactID != 0 || a.Expired || a.ID <= 0 {
					return receipt, errors.New("ambiguous or expired result artifact")
				}
				artifactID = a.ID
			}
		}
		if len(list.Artifacts) < 100 {
			break
		}
		if page == 10 {
			return receipt, errors.New("too many result artifacts")
		}
	}
	if artifactID == 0 {
		return receipt, errors.New("remote workflow produced no result receipt")
	}
	data, err := g.call(ctx, "GET", fmt.Sprintf("/repos/%s/actions/artifacts/%d/zip", repo, artifactID), nil)
	if err != nil {
		return receipt, err
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) != 1 || archive.File[0].Name != "receipt.json" || !archive.File[0].Mode().IsRegular() || archive.File[0].UncompressedSize64 > 2<<20 {
		return receipt, errors.New("invalid remote result archive")
	}
	stream, err := archive.File[0].Open()
	if err != nil {
		return receipt, errors.New("cannot read result receipt")
	}
	defer stream.Close()
	data, err = io.ReadAll(io.LimitReader(stream, (2<<20)+1))
	if err != nil || len(data) > 2<<20 || json.Unmarshal(data, &receipt) != nil {
		return receipt, errors.New("invalid result receipt")
	}
	if err := validateRuntimeReceipt(receipt, request); err != nil {
		return receipt, err
	}
	if receipt.RequestID != request.RequestID || receipt.ScenarioID != request.ScenarioID || receipt.PackageSHA != request.PackageSHA || receipt.RuntimeSHA != sha || receipt.RunID != run.ID || receipt.RunAttempt != 1 || receipt.Result.Schema != "spex.result/v1" {
		return receipt, errors.New("result receipt identity mismatch")
	}
	return receipt, nil
}

func validateRuntimeReceipt(receipt remoteReceipt, request remoteRequest) error {
	if request.Schema == "spex.submission/v1" {
		if receipt.Schema == "spex.remote-result/v1" && receipt.Result.ScenarioID == request.ScenarioID && receipt.Result.Runtime == request.Runtime {
			return nil
		}
		return errors.New("result receipt identity mismatch")
	}
	requested, err := scenario.ParseRuntimeSelector(request.Runtime)
	resolved, resolvedErr := scenario.ParseRuntimeSelector(receipt.Result.Runtime + "@" + receipt.RuntimeRelease)
	if request.Schema != "spex.submission/v2" || receipt.Schema != "spex.remote-result/v2" || err != nil || resolvedErr != nil || resolved.Contract == "" || resolved.Release == "latest" || !hex64Pattern.MatchString(receipt.ResolvedScenarioID) || receipt.Result.ScenarioID != receipt.ResolvedScenarioID || receipt.Result.RuntimeRelease != receipt.RuntimeRelease {
		return errors.New("invalid resolved runtime receipt")
	}
	if requested.Contract != "" && requested.Contract != resolved.Contract {
		return errors.New("receiver selected a different runtime")
	}
	if requested.Release != "" && requested.Release != "latest" && requested.Release != resolved.Release {
		return errors.New("receiver selected a different runtime release")
	}
	return nil
}

// parseRuntimeWorkflow accepts an explicit GitHub workflow reference, not a URL.
// Keep dispatch coordinates in Spex so every authoring adapter uses the same rules.
func parseRuntimeWorkflow(value string) (string, string, string, error) {
	path, ref, ok := strings.Cut(value, "@")
	repo, workflow, found := strings.Cut(path, "/.github/workflows/")
	if !ok || !found || !repositoryPattern.MatchString(repo) || !workflowPattern.MatchString(workflow) || ref == "" || strings.ContainsAny(ref, "@ \t\r\n") {
		return "", "", "", errors.New("runtime-workflow must be owner/repository/.github/workflows/file.yaml@ref")
	}
	return repo, workflow, ref, nil
}

func runRemoteSubmission(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scenario submit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("workspace", ".", "caller workspace")
	requestFile := fs.String("request", "", "prepared request metadata")
	repo := fs.String("repository", "", "remote owner/repository")
	workflow := fs.String("workflow", "", "remote workflow filename")
	ref := fs.String("ref", "", "explicit remote branch or tag")
	runtimeWorkflow := fs.String("runtime-workflow", "", "owner/repository/.github/workflows/file.yaml@ref")
	sourceRepo := fs.String("source-repository", "", "repository containing uploaded package")
	sourceRun := fs.Int64("source-run-id", 0, "caller workflow run")
	artifactID := fs.Int64("artifact-id", 0, "uploaded package artifact")
	wait := fs.Duration("wait-timeout", 6*time.Hour, "total dispatch and execution wait")
	if fs.Parse(args) != nil {
		return ExitError{Code: 2, Err: errors.New("invalid remote submission arguments")}
	}
	combined, separate := false, false
	fs.Visit(func(f *flag.Flag) {
		combined = combined || f.Name == "runtime-workflow"
		separate = separate || f.Name == "repository" || f.Name == "workflow" || f.Name == "ref"
	})
	if combined {
		if separate {
			return ExitError{Code: 2, Err: errors.New("runtime-workflow cannot be combined with repository, workflow or ref")}
		}
		var err error
		*repo, *workflow, *ref, err = parseRuntimeWorkflow(*runtimeWorkflow)
		if err != nil {
			return ExitError{Code: 2, Err: err}
		}
	}
	if fs.NArg() != 0 || !repositoryPattern.MatchString(*repo) || !repositoryPattern.MatchString(*sourceRepo) || !workflowPattern.MatchString(*workflow) || strings.TrimSpace(*ref) == "" || strings.ContainsAny(*ref, "\r\n") || *sourceRun <= 0 || *artifactID <= 0 || *wait <= 0 || *wait > 24*time.Hour {
		return ExitError{Code: 2, Err: errors.New("invalid remote submission arguments")}
	}
	token := os.Getenv("SPEX_REMOTE_TOKEN")
	if token == "" {
		return ExitError{Code: 2, Err: errors.New("SPEX_REMOTE_TOKEN is required")}
	}
	path, err := scenario.SourcePath(*root, *requestFile)
	if err != nil {
		return err
	}
	data, err := readRegularEvidenceFile(path, 2<<20)
	var request remoteRequest
	if err != nil || json.Unmarshal(data, &request) != nil || (request.Schema != "spex.submission/v1" && request.Schema != "spex.submission/v2") || !hex32Pattern.MatchString(request.RequestID) || !hex64Pattern.MatchString(request.ScenarioID) || !hex64Pattern.MatchString(request.PackageSHA) {
		return errors.New("invalid submission metadata")
	}
	if _, err := scenario.ParseRuntimeSelector(request.Runtime); err != nil {
		return errors.New("invalid requested runtime")
	}
	packagePath, err := scenario.SourcePath(*root, request.PackagePath)
	if err != nil {
		return err
	}
	data, err = readRegularEvidenceFile(packagePath, 32<<20)
	if err != nil || hex.EncodeToString(sha256Sum(data)) != request.PackageSHA {
		return errors.New("package changed after preparation")
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" {
			return errors.New("unsafe download redirect")
		}
		req.Header.Del("Authorization")
		return nil
	}}
	g := githubRemote{base: "https://api.github.com", token: token, client: client, poll: 10 * time.Second}
	return submitRemote(ctx, g, request, *root, *repo, *workflow, *ref, *sourceRepo, *sourceRun, *artifactID, *wait, out)
}

func submitRemote(parent context.Context, g githubRemote, request remoteRequest, root, repo, workflow, ref, sourceRepo string, sourceRun, artifactID int64, wait time.Duration, out io.Writer) (retErr error) {
	ctx, cancel := context.WithTimeout(parent, wait)
	defer cancel()
	data, err := g.call(ctx, "GET", "/repos/"+repo+"/commits/"+url.PathEscape(ref), nil)
	var commit struct {
		SHA string `json:"sha"`
	}
	if err != nil || json.Unmarshal(data, &commit) != nil || !shaPattern.MatchString(commit.SHA) {
		return errors.New("cannot resolve runtime workflow revision")
	}
	data, err = g.call(ctx, "GET", fmt.Sprintf("/repos/%s/actions/artifacts/%d", sourceRepo, artifactID), nil)
	var artifact struct {
		Name    string `json:"name"`
		Expired bool   `json:"expired"`
		Run     struct {
			ID int64 `json:"id"`
		} `json:"workflow_run"`
	}
	if err != nil || json.Unmarshal(data, &artifact) != nil || artifact.Expired || artifact.Name != "spex-request-"+request.RequestID || artifact.Run.ID != sourceRun {
		return errors.New("package artifact does not match submission")
	}
	base := filepath.Dir(request.RequestPath)
	// Exclusive receipt prevents a retry from silently dispatching the same request twice.
	dispatchRecord, _ := json.Marshal(struct {
		Schema           string `json:"schema"`
		Status           string `json:"status"`
		Repository       string `json:"repository"`
		Workflow         string `json:"workflow"`
		RuntimeSHA       string `json:"runtime_sha"`
		RequestID        string `json:"request_id"`
		SourceRepository string `json:"source_repository"`
		SourceRunID      int64  `json:"source_run_id"`
		ArtifactID       int64  `json:"artifact_id"`
	}{"spex.dispatch/v1", "attempted", repo, workflow, commit.SHA, request.RequestID, sourceRepo, sourceRun, artifactID})
	if err := writeCanonicalScenario(root, filepath.Join(base, "dispatch.json"), dispatchRecord); err != nil {
		return err
	}
	var run remoteRun
	remoteURL := ""
	resolvedRuntime, resolvedRelease, resolvedID := request.Runtime, "", ""
	defer func() {
		cancelStatus := "not_requested"
		if ctx.Err() != nil || (retErr != nil && run.Status != "completed") {
			cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			if run.ID == 0 {
				run, _ = g.lookup(cleanup, repo, workflow, request.RequestID, commit.SHA)
			}
			cancelStatus = "unconfirmed"
			if run.ID != 0 {
				if _, err := g.call(cleanup, "POST", fmt.Sprintf("/repos/%s/actions/runs/%d/cancel", repo, run.ID), nil); err == nil {
					cancelStatus = "requested"
				}
			}
			if ctx.Err() != nil && ExitCode(retErr) != ExitRuntime {
				retErr = ExitError{Code: 130, Err: errors.New("remote wait cancelled or expired; inspect cancellation status")}
			}
		}
		if run.ID != 0 {
			remoteURL = fmt.Sprintf("https://github.com/%s/actions/runs/%d", repo, run.ID)
		}
		status := struct {
			RequestID          string `json:"request_id"`
			ScenarioID         string `json:"scenario_id"`
			Runtime            string `json:"runtime"`
			RuntimeRelease     string `json:"runtime_release,omitempty"`
			ResolvedScenarioID string `json:"resolved_scenario_id,omitempty"`
			RuntimeSHA         string `json:"runtime_sha"`
			RunID              int64  `json:"run_id"`
			RunURL             string `json:"run_url"`
			Cancellation       string `json:"cancellation"`
			ResultPath         string `json:"result_path"`
			ArtifactDirectory  string `json:"artifact_directory"`
		}{request.RequestID, request.ScenarioID, resolvedRuntime, resolvedRelease, resolvedID, commit.SHA, run.ID, remoteURL, cancelStatus, filepath.Join(root, base, "result.json"), filepath.Join(root, base)}
		if info, err := os.Stat(status.ResultPath); err != nil || !info.Mode().IsRegular() {
			status.ResultPath = ""
		}
		encoded, _ := json.Marshal(status)
		if err := writeCanonicalScenario(root, filepath.Join(base, "remote.json"), encoded); err != nil && retErr == nil {
			retErr = err
		}
		if err := json.NewEncoder(out).Encode(status); err != nil && retErr == nil {
			retErr = err
		}
	}()
	inputs := map[string]string{"request_id": request.RequestID, "scenario_id": request.ScenarioID, "package_sha256": request.PackageSHA, "runtime": request.Runtime, "source_repository": sourceRepo, "source_run_id": strconv.FormatInt(sourceRun, 10), "source_artifact_id": strconv.FormatInt(artifactID, 10)}
	if request.Schema == "spex.submission/v2" {
		inputs["submission_schema"] = request.Schema
	}
	_, dispatchErr := g.call(ctx, "POST", "/repos/"+repo+"/actions/workflows/"+workflow+"/dispatches", map[string]any{"ref": ref, "inputs": inputs})
	// Do not retry dispatch: a lost acknowledgement must not create another run.
	if dispatchErr != nil {
		return errors.New("dispatch not confirmed; request identity retained; do not blindly resubmit")
	}
	for {
		run, err = g.lookup(ctx, repo, workflow, request.RequestID, commit.SHA)
		if err != nil {
			return err
		}
		if run.ID != 0 && run.Status == "completed" {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(g.poll):
		}
	}
	receipt, err := g.result(ctx, repo, run, request, commit.SHA)
	if err != nil {
		return err
	}
	resolvedRuntime = receipt.Result.Runtime
	if request.Schema == "spex.submission/v2" {
		resolvedRelease, resolvedID = receipt.RuntimeRelease, receipt.Result.ScenarioID
	}
	// Discard untrusted free-form problem messages and artifact paths.
	for i := range receipt.Result.Problems {
		receipt.Result.Problems[i] = scenarioruntime.Problem{Phase: "remote", Code: "receiver_problem", Message: "Inspect remote evidence"}
	}
	receipt.Result.Artifacts = nil
	for i := range receipt.Result.Tests {
		switch receipt.Result.Tests[i].Outcome {
		case scenarioruntime.Passed, scenarioruntime.Failed, scenarioruntime.Error, scenarioruntime.Cancelled:
		default:
			return errors.New("invalid remote test outcome")
		}
		receipt.Result.Tests[i].Name = fmt.Sprintf("test-%d", i)
	}
	switch receipt.Result.Cleanup {
	case "succeeded", "not_run", "failed", "incomplete":
	default:
		return errors.New("invalid remote cleanup outcome")
	}
	switch receipt.Result.Outcome {
	case scenarioruntime.Passed, scenarioruntime.Failed, scenarioruntime.Error, scenarioruntime.Cancelled:
	default:
		return errors.New("invalid remote outcome")
	}
	if receipt.Result.Outcome == scenarioruntime.Passed {
		consistent := run.Conclusion == "success" && len(receipt.Result.Problems) == 0 && receipt.PlannedTests > 0 && len(receipt.Result.Tests) == receipt.PlannedTests && (receipt.Result.Cleanup == "succeeded" || receipt.Result.Cleanup == "not_run")
		for _, test := range receipt.Result.Tests {
			consistent = consistent && test.Outcome == scenarioruntime.Passed
		}
		if !consistent {
			receipt.Result.Outcome = scenarioruntime.Error
			receipt.Result.Problems = append(receipt.Result.Problems, scenarioruntime.Problem{Phase: "remote", Code: "inconsistent_receiver_result", Message: "Remote execution did not establish complete success"})
		}
	}
	result, err := json.Marshal(receipt.Result)
	if err != nil {
		return errors.New("cannot encode remote result")
	}
	if err = writeCanonicalScenario(root, filepath.Join(base, "result.json"), result); err != nil {
		return err
	}
	switch receipt.Result.Outcome {
	case scenarioruntime.Passed:
		return nil
	case scenarioruntime.Failed:
		return ExitError{Code: 3, Err: errors.New("remote acceptance tests failed")}
	case scenarioruntime.Cancelled:
		return ExitError{Code: 130, Err: errors.New("remote execution cancelled")}
	default:
		return ExitError{Code: 4, Err: errors.New("remote execution failed")}
	}
}
