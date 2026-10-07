package spex

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestRuntimeWorkflowReference(t *testing.T) {
	for _, ref := range []string{"v1", "main", "feature/runtime"} {
		repo, workflow, got, err := parseRuntimeWorkflow("owner/runtime/.github/workflows/acceptance.yaml@" + ref)
		if err != nil || repo != "owner/runtime" || workflow != "acceptance.yaml" || got != ref {
			t.Fatalf("unexpected parsed destination: %q %q %q %v", repo, workflow, got, err)
		}
	}
	for _, value := range []string{"", "owner/runtime", "owner/runtime/.github/workflows/acceptance.yaml", "owner/runtime/.github/workflows/acceptance.yaml@", "https://github.com/owner/runtime/.github/workflows/acceptance.yaml@v1", "owner/runtime/.github/workflows/nested/acceptance.yaml@v1", "owner/runtime/.github/workflows/acceptance.yaml@v1@other", "owner/runtime/.github/workflows/acceptance.yaml@bad ref"} {
		if _, _, _, err := parseRuntimeWorkflow(value); err == nil {
			t.Errorf("accepted invalid reference %q", value)
		}
	}
}

func TestRuntimeWorkflowRejectsSeparateFlags(t *testing.T) {
	for _, flag := range []string{"--repository", "--workflow", "--ref"} {
		err := runRemoteSubmission(context.Background(), []string{"--runtime-workflow", "owner/runtime/.github/workflows/acceptance.yml@v1", flag, ""}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("expected conflicting flags error, got %v", err)
		}
	}
}
