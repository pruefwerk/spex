package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pruefwerk/spex/pkg/receiver"
	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
)

type sourceAPI interface {
	Get(context.Context, string, any) error
	Download(context.Context, string, int64) ([]byte, error)
}

// Explicit tags distinguish the repository from the fork/head repository.
type runMetadata struct {
	ID         int64 `json:"id"`
	Attempt    int   `json:"run_attempt"`
	Status     string
	Conclusion json.RawMessage
	Event      string
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	HeadRepository struct {
		FullName string `json:"full_name"`
	} `json:"head_repository"`
	SHA      string `json:"head_sha"`
	Branch   string `json:"head_branch"`
	Actor    struct{ Login string }
	Workflow int64 `json:"workflow_id"`
}

func (h *Host) authenticateSource(ctx context.Context, env func(string) string, policy hostconfig.SourcePolicy, api sourceAPI, now time.Time) (runMetadata, error) {
	fail := errors.New("source identity not admitted")
	if env("TRANSPORT_SCHEMA") != "spex.transport/v1" || env("GITHUB_RUN_ATTEMPT") != "1" || !requestID.MatchString(env("REQUEST_ID")) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(env("REQUEST_SHA256")) || !repositoryID.MatchString(env("SOURCE_REPOSITORY")) {
		return runMetadata{}, fail
	}
	number := regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	if !number.MatchString(env("SOURCE_RUN_ID")) || !number.MatchString(env("SOURCE_ARTIFACT_ID")) {
		return runMetadata{}, fail
	}
	runID, err := strconv.ParseInt(env("SOURCE_RUN_ID"), 10, 64)
	if err != nil {
		return runMetadata{}, fail
	}
	artifactID, err := strconv.ParseInt(env("SOURCE_ARTIFACT_ID"), 10, 64)
	if err != nil {
		return runMetadata{}, fail
	}
	repository := env("SOURCE_REPOSITORY")
	entries := []hostconfig.SourceEntry{}
	if !h.resolver.IsSourcePolicySchema(policy.Schema) || policy.Validate() != nil {
		return runMetadata{}, fail
	}
	for _, entry := range policy.Sources {
		if strings.EqualFold(entry.Repository, repository) ||
			(entry.Organization != "" && strings.EqualFold(entry.Organization, strings.SplitN(repository, "/", 2)[0])) {
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		return runMetadata{}, fail
	}
	// Read ownership and visibility from GitHub, never from dispatch inputs or
	// the source artifact. Missing metadata must not relax a visibility rule.
	var source struct {
		FullName   string `json:"full_name"`
		Visibility string
		Owner      struct{ Login, Type string }
	}
	needsMetadata := false
	for _, entry := range entries {
		needsMetadata = needsMetadata || entry.Organization != "" || entry.Visibilities != nil
	}
	if needsMetadata {
		if api.Get(ctx, fmt.Sprintf("/repos/%s", repository), &source) != nil || !strings.EqualFold(source.FullName, repository) {
			return runMetadata{}, fail
		}
		filtered := entries[:0]
		for _, entry := range entries {
			if entry.Organization != "" && (source.Owner.Type != "Organization" || !strings.EqualFold(source.Owner.Login, entry.Organization)) {
				continue
			}
			if entry.Visibilities != nil && !slices.Contains(entry.Visibilities, source.Visibility) {
				continue
			}
			filtered = append(filtered, entry)
		}
		entries = filtered
		if len(entries) == 0 {
			return runMetadata{}, fail
		}
	}
	var run runMetadata
	if api.Get(ctx, fmt.Sprintf("/repos/%s/actions/runs/%d", repository, runID), &run) != nil || run.ID != runID || run.Attempt != 1 || run.Status != "in_progress" || string(run.Conclusion) != "null" || (run.Event != "push" && run.Event != "workflow_dispatch") || run.Repository.FullName != repository || run.HeadRepository.FullName != repository || !commitID.MatchString(run.SHA) || run.Workflow < 1 {
		return runMetadata{}, fail
	}
	var workflow struct{ Path string }
	if api.Get(ctx, fmt.Sprintf("/repos/%s/actions/workflows/%d", repository, run.Workflow), &workflow) != nil {
		return runMetadata{}, fail
	}
	allowed := false
	for _, entry := range entries {
		if (entry.Workflow == "" || entry.Workflow == workflow.Path) &&
			(entry.Branch == "" || entry.Branch == run.Branch) &&
			(entry.Actors == nil || slices.Contains(entry.Actors, run.Actor.Login)) {
			allowed = true
		}
	}
	if !allowed {
		return runMetadata{}, fail
	}
	var artifact struct {
		ID      int64
		Name    string
		Expired *bool
		Size    int64  `json:"size_in_bytes"`
		Created string `json:"created_at"`
		Run     struct {
			ID  int64
			SHA string `json:"head_sha"`
		} `json:"workflow_run"`
	}
	if api.Get(ctx, fmt.Sprintf("/repos/%s/actions/artifacts/%d", repository, artifactID), &artifact) != nil || artifact.ID != artifactID || artifact.Name != "spex-request-"+env("REQUEST_ID") || artifact.Expired == nil || *artifact.Expired || artifact.Size < 1 || artifact.Size > 8<<20 || artifact.Run.ID != run.ID || artifact.Run.SHA != run.SHA {
		return runMetadata{}, fail
	}
	created, err := time.Parse(time.RFC3339, artifact.Created)
	if err != nil || created.Before(now.Add(-24*time.Hour)) || created.After(now.Add(5*time.Minute)) {
		return runMetadata{}, fail
	}
	return run, nil
}

func (h *Host) admitSource(ctx context.Context, root string, env func(string) string, api sourceAPI) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	policy, err := h.resolver.ReadSourcePolicy(filepath.Join(root, h.config.SourcePolicy))
	if err != nil {
		return err
	}
	run, err := h.authenticateSource(ctx, env, policy, api, time.Now())
	if err != nil {
		return err
	}
	private := filepath.Join(root, ".spex/receiver-private")
	if err := os.MkdirAll(filepath.Dir(private), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(private, 0700); err != nil {
		return err
	}
	artifact, err := api.Download(ctx, fmt.Sprintf("/repos/%s/actions/artifacts/%s/zip", env("SOURCE_REPOSITORY"), env("SOURCE_ARTIFACT_ID")), 8<<20)
	if err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(private, "request.zip"), artifact); err != nil {
		return err
	}
	source, err := api.Download(ctx, fmt.Sprintf("/repos/%s/tarball/%s", env("SOURCE_REPOSITORY"), run.SHA), 64<<20)
	if err != nil {
		return err
	}
	if err := receiver.ExtractSource(ctx, source, filepath.Join(private, "source")); err != nil {
		return err
	}
	workerRun, err := strconv.ParseInt(env("GITHUB_RUN_ID"), 10, 64)
	if err != nil {
		return err
	}
	scope, err := h.resolver.Allocate(env("GITHUB_REPOSITORY"), workerRun, 1, env("REQUEST_ID"), "kind")
	if err != nil {
		return err
	}
	name, _ := scope.Name()
	scopePath := filepath.Join(root, ".spex/execution-scopes", name+".json")
	if err := os.MkdirAll(filepath.Dir(scopePath), 0700); err != nil {
		return err
	}
	if err := hostconfig.SaveScope(scopePath, scope); err != nil {
		return err
	}
	suite, err := h.configureKind(root, scopePath)
	if err != nil {
		return err
	}
	admitted := admissionContext{Schema: h.resolver.Protocol("receiver-admission"), Expected: receiver.Expected{RequestID: env("REQUEST_ID"), SHA256: env("REQUEST_SHA256"), Repository: env("SOURCE_REPOSITORY"), Commit: run.SHA, SourceRunID: run.ID}, Checkout: receiver.Checkout{Directory: filepath.Join(private, "source"), Repository: env("SOURCE_REPOSITORY"), Commit: run.SHA}, Artifact: filepath.Join(private, "request.zip"), Root: root, Suite: suite, Scope: scopePath, Kubeconfig: filepath.Join(root, ".spex/isolated", name, "kubeconfig"), Actor: run.Actor.Login}
	data, err := json.Marshal(admitted)
	if err != nil {
		return err
	}
	path := filepath.Join(private, "context.json")
	if err := writePrivate(path, data); err != nil {
		return err
	}
	return appendCI(env("GITHUB_OUTPUT"), "context-path="+path+"\nscope-path="+scopePath+"\n")
}
