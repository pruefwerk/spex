package command

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseCommitUsesSelectedTag(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(string(out), err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "release")
	first := git("rev-parse", "HEAD")
	git("tag", "v0.1.0-rc.1")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "workflow")
	workflow := git("rev-parse", "HEAD")
	script, err := filepath.Abs("../../scripts/release_commit.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, matching := range []bool{false, true} {
		if matching {
			git("checkout", "--quiet", "--detach", "v0.1.0-rc.1")
		}
		cmd := exec.Command("bash", script, "v0.1.0-rc.1")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GITHUB_SHA="+workflow)
		out, err := cmd.CombinedOutput()
		if matching {
			if err != nil || strings.TrimSpace(string(out)) != first {
				t.Fatal(string(out), err)
			}
		} else if err == nil {
			t.Fatal("mismatched checkout accepted")
		}
	}
}

func TestReleaseWorkflowUsesResolvedCommitAndQualityGates(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string
				Run  string
				Env  map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	resolved, quality, builds := false, false, 0
	for _, step := range workflow.Jobs["release"].Steps {
		if strings.Contains(step.Run, "release_commit.sh") {
			resolved = true
		}
		if step.Uses == "actions/setup-go@v5" && !resolved {
			t.Fatal("Go version selected before release checkout")
		}
		if strings.Contains(step.Run, "go test -race ./...") && strings.Contains(step.Run, "go vet ./...") {
			quality = true
		}
		if strings.Contains(step.Run, "COMMIT=") {
			builds++
			if !strings.Contains(step.Run, `COMMIT="${RELEASE_COMMIT}"`) || step.Env["RELEASE_COMMIT"] != "${{ steps.meta.outputs.commit }}" {
				t.Fatal("build uses workflow commit", step.Run)
			}
		}
	}
	if !resolved || !quality || builds != 2 {
		t.Fatal("release gates missing")
	}
}

func TestCIRequiresQualityBeforeCandidateQualification(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/ci.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct{ Run string }
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	quality, candidate := false, false
	for _, step := range workflow.Jobs["test"].Steps {
		if strings.TrimSpace(step.Run) == "make quality-check" {
			quality = true
		}
		if strings.Contains(step.Run, "make production-candidate-check") {
			candidate = true
			if !quality {
				t.Fatal("candidate qualified without the race/vet gate")
			}
		}
	}
	if !quality || !candidate {
		t.Fatal("required CI qualification missing")
	}
}
