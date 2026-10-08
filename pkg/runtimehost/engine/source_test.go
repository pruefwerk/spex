package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
)

type admissionAPI struct {
	documents map[string]string
	calls     []string
}

func (a *admissionAPI) Get(_ context.Context, path string, value any) error {
	a.calls = append(a.calls, path)
	document, ok := a.documents[path]
	if !ok {
		return errors.New("unavailable")
	}
	return json.Unmarshal([]byte(document), value)
}
func (*admissionAPI) Download(context.Context, string, int64) ([]byte, error) {
	return nil, errors.New("authentication must not download")
}

func TestSourceAdmissionOrganizationBoundary(t *testing.T) {
	_, file := configuredFixture(t)
	h, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	const repoPath = "/repos/acme/service"
	const runPath = repoPath + "/actions/runs/12"
	const workflowPath = repoPath + "/actions/workflows/7"
	const artifactPath = repoPath + "/actions/artifacts/34"
	sha := strings.Repeat("a", 40)
	// Use a real request identifier accepted by the transport validator.
	id := "0123456789abcdef0123456789abcdef"
	values := map[string]string{"TRANSPORT_SCHEMA": "spex.transport/v1", "GITHUB_RUN_ATTEMPT": "1", "REQUEST_ID": id, "REQUEST_SHA256": strings.Repeat("b", 64), "SOURCE_REPOSITORY": "acme/service", "SOURCE_RUN_ID": "12", "SOURCE_ARTIFACT_ID": "34"}
	base := map[string]string{
		repoPath:     `{"full_name":"acme/service","visibility":"internal","owner":{"login":"acme","type":"Organization"}}`,
		runPath:      `{"id":12,"run_attempt":1,"status":"in_progress","conclusion":null,"event":"push","repository":{"full_name":"acme/service"},"head_repository":{"full_name":"acme/service"},"head_sha":"` + sha + `","head_branch":"feature/change","actor":{"login":"developer"},"workflow_id":7}`,
		workflowPath: `{"path":".github/workflows/acceptance.yaml"}`,
		artifactPath: `{"id":34,"name":"spex-request-` + id + `","expired":false,"size_in_bytes":100,"created_at":"2026-10-09T11:59:00Z","workflow_run":{"id":12,"head_sha":"` + sha + `"}}`,
	}
	organization := hostconfig.SourceEntry{Organization: "acme", Visibilities: []string{"internal", "private"}}
	for _, tc := range []struct {
		name           string
		entry          hostconfig.SourceEntry
		path, document string
		pass           bool
	}{
		{name: "internal arbitrary branch and actor", entry: organization, pass: true},
		{name: "private", entry: organization, path: repoPath, document: strings.Replace(base[repoPath], "internal", "private", 1), pass: true},
		{name: "case insensitive organization", entry: hostconfig.SourceEntry{Organization: "ACME", Visibilities: []string{"internal"}}, pass: true},
		{name: "public", entry: organization, path: repoPath, document: strings.Replace(base[repoPath], "internal", "public", 1)},
		{name: "unknown visibility", entry: organization, path: repoPath, document: strings.Replace(base[repoPath], "internal", "", 1)},
		{name: "personal owner", entry: organization, path: repoPath, document: strings.Replace(base[repoPath], "Organization", "User", 1)},
		{name: "different owner", entry: organization, path: repoPath, document: strings.Replace(base[repoPath], `"login":"acme"`, `"login":"other"`, 1)},
		{name: "different repository", entry: organization, path: repoPath, document: strings.Replace(base[repoPath], "acme/service", "acme/other", 1)},
		{name: "metadata unavailable", entry: organization, path: repoPath, document: ""},
		{name: "outside organization", entry: hostconfig.SourceEntry{Organization: "other", Visibilities: []string{"internal"}}},
		{name: "optional restrictions match", entry: hostconfig.SourceEntry{Organization: "acme", Visibilities: []string{"internal"}, Workflow: ".github/workflows/acceptance.yaml", Branch: "feature/change", Actors: []string{"developer"}}, pass: true},
		{name: "workflow restricted", entry: hostconfig.SourceEntry{Organization: "acme", Visibilities: []string{"internal"}, Workflow: ".github/workflows/release.yaml"}},
		{name: "branch restricted", entry: hostconfig.SourceEntry{Organization: "acme", Visibilities: []string{"internal"}, Branch: "main"}},
		{name: "actor restricted", entry: hostconfig.SourceEntry{Organization: "acme", Visibilities: []string{"internal"}, Actors: []string{"bot"}}},
		{name: "legacy exact policy", entry: hostconfig.SourceEntry{Repository: "acme/service", Workflow: ".github/workflows/acceptance.yaml", Branch: "feature/change", Actors: []string{"developer"}}, pass: true},
		{name: "repository only", entry: hostconfig.SourceEntry{Repository: "acme/service"}, pass: true},
		{name: "fork excluded", entry: organization, path: runPath, document: strings.Replace(base[runPath], `"head_repository":{"full_name":"acme/service"}`, `"head_repository":{"full_name":"other/fork"}`, 1)},
		{name: "pull request excluded", entry: organization, path: runPath, document: strings.Replace(base[runPath], `"event":"push"`, `"event":"pull_request"`, 1)},
		{name: "rerun excluded", entry: organization, path: runPath, document: strings.Replace(base[runPath], `"run_attempt":1`, `"run_attempt":2`, 1)},
		{name: "completed run excluded", entry: organization, path: runPath, document: strings.Replace(base[runPath], "in_progress", "completed", 1)},
		{name: "artifact from different run", entry: organization, path: artifactPath, document: strings.Replace(base[artifactPath], `"id":12`, `"id":13`, 1)},
		{name: "artifact from different commit", entry: organization, path: artifactPath, document: strings.Replace(base[artifactPath], sha, strings.Repeat("c", 40), 1)},
		{name: "expired artifact", entry: organization, path: artifactPath, document: strings.Replace(base[artifactPath], `"expired":false`, `"expired":true`, 1)},
		{name: "stale artifact", entry: organization, path: artifactPath, document: strings.Replace(base[artifactPath], "2026-10-09", "2026-10-07", 1)},
		{name: "malformed policy", entry: hostconfig.SourceEntry{Organization: "acme"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &admissionAPI{documents: map[string]string{}}
			for key, value := range base {
				api.documents[key] = value
			}
			if tc.path != "" {
				if tc.document == "" {
					delete(api.documents, tc.path)
				} else {
					api.documents[tc.path] = tc.document
				}
			}
			_, err := h.authenticateSource(context.Background(), func(key string) string { return values[key] }, hostconfig.SourcePolicy{Schema: hostconfig.SourcePolicySchema, Sources: []hostconfig.SourceEntry{tc.entry}}, api, now)
			if (err == nil) != tc.pass {
				t.Fatalf("admission error=%v, want admitted=%v", err, tc.pass)
			}
			if (tc.name == "outside organization" || tc.name == "malformed policy") && len(api.calls) != 0 {
				t.Fatal("untrusted selector made API calls")
			}
		})
	}
	for _, key := range []string{"REQUEST_ID", "REQUEST_SHA256", "SOURCE_RUN_ID", "SOURCE_ARTIFACT_ID", "GITHUB_RUN_ATTEMPT"} {
		t.Run("invalid "+key, func(t *testing.T) {
			api := &admissionAPI{documents: base}
			_, err := h.authenticateSource(context.Background(), func(k string) string {
				if k == key {
					return "invalid"
				}
				return values[k]
			}, hostconfig.SourcePolicy{Schema: hostconfig.SourcePolicySchema, Sources: []hostconfig.SourceEntry{organization}}, api, now)
			if err == nil || len(api.calls) != 0 {
				t.Fatal("invalid transport reached GitHub")
			}
		})
	}
}
