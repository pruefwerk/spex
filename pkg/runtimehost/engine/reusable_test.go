package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
)

func TestReusableAdmissionChecksTheCurrentCallerRun(t *testing.T) {
	_, file := configuredFixture(t)
	h, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	id := "0123456789abcdef0123456789abcdef"
	values := map[string]string{"GITHUB_RUN_ID": "12", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_REPOSITORY": "acme/service", "GITHUB_SHA": sha, "GITHUB_EVENT_NAME": "push", "GITHUB_ACTOR": "developer", "GITHUB_TOKEN": "PRIVATE_SENTINEL", "REQUEST_ID": id, "REQUEST_SHA256": strings.Repeat("b", 64), "SOURCE_ARTIFACT_ID": "34", "SOURCE_REPOSITORY": "untrusted/other", "SOURCE_RUN_ID": "99", "SOURCE_READ_TOKEN": "UNTRUSTED_TOKEN"}
	documents := map[string]string{
		"/repos/acme/service/actions/runs/12":      `{"id":12,"run_attempt":1,"status":"in_progress","conclusion":null,"event":"push","repository":{"full_name":"acme/service"},"head_repository":{"full_name":"acme/service"},"head_sha":"` + sha + `","actor":{"login":"developer"},"workflow_id":7}`,
		"/repos/acme/service/actions/workflows/7":  `{"path":".github/workflows/release.yaml"}`,
		"/repos/acme/service/actions/artifacts/34": `{"id":34,"name":"spex-request-` + id + `","expired":false,"size_in_bytes":100,"created_at":"2026-10-09T11:59:00Z","workflow_run":{"id":12,"head_sha":"` + sha + `"}}`,
	}
	policy := hostconfig.SourcePolicy{Schema: hostconfig.SourcePolicySchema, Sources: []hostconfig.SourceEntry{{Repository: "acme/service"}}}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		key, value string
		pass       bool
	}{{pass: true}, {"GITHUB_SHA", strings.Repeat("c", 40), false}, {"GITHUB_EVENT_NAME", "pull_request", false}, {"GITHUB_ACTOR", "other", false}, {"GITHUB_RUN_ID", "99", false}, {"GITHUB_REPOSITORY", "acme/other", false}, {"GITHUB_RUN_ATTEMPT", "2", false}} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			env := reusableSourceEnvironment(func(key string) string {
				if key == tc.key {
					return tc.value
				}
				return values[key]
			})
			if env("SOURCE_READ_TOKEN") != "PRIVATE_SENTINEL" {
				t.Fatal("caller credentials not selected")
			}
			api := &admissionAPI{documents: documents}
			_, err := h.authenticateSource(context.Background(), env, policy, api, now)
			if (err == nil) != tc.pass {
				t.Fatalf("admission error %v; expected pass %v", err, tc.pass)
			}
			for _, path := range api.calls {
				if strings.Contains(path, "untrusted") {
					t.Fatal("input selected another source")
				}
			}
		})
	}
}
