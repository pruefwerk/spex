package spex

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const maxEvidenceLogSize int64 = 16 << 20
const maxEvidenceStatusSize int64 = 1 << 20
const maxEvidenceResourceSize int64 = 1 << 20

func collectEvidence(command, workspacePath string, stepMap stepMapFile) {
	logDir := filepath.Join(workspacePath, "evidence", "logs")
	resultDir := filepath.Join(workspacePath, "evidence", "results")
	statusDir := filepath.Join(workspacePath, "evidence", "status")
	if err := ensureSafeDirectory(logDir, 0o755); err != nil {
		return
	}
	if err := ensureSafeDirectory(resultDir, 0o755); err != nil {
		return
	}
	if err := ensureSafeDirectory(statusDir, 0o755); err != nil {
		return
	}
	for _, step := range stepMap.Spec.Steps {
		ordinal := twoDigit(step.Ordinal)
		statusArgs := kubectlArgsForWorkspace(workspacePath, stepMap.Spec.KubeContext, "-n", stepMap.Spec.Namespace, "get", "job", step.JobName, "-o", "json")
		statusOutput, statusErr := runBoundedCommand(maxEvidenceStatusSize, command, statusArgs...)
		if statusErr == nil && len(statusOutput) > 0 {
			statusPath := filepath.Join(statusDir, ordinal+"-"+step.OperationID+".job.json")
			_ = writeEvidenceFile(statusPath, statusOutput)
		}

		selector := podLogSelector(step)
		args := kubectlArgsForWorkspace(workspacePath, stepMap.Spec.KubeContext, "-n", stepMap.Spec.Namespace, "logs", "-l", selector)
		output, err := runBoundedCommand(maxEvidenceLogSize, command, args...)
		if err != nil {
			continue
		}
		logPath := filepath.Join(logDir, ordinal+"-"+step.OperationID+".log")
		resultPath := filepath.Join(resultDir, ordinal+"-"+step.OperationID+".jsonl")
		if err := writeEvidenceFile(logPath, output); err != nil {
			continue
		}
		results := probeResultLines(string(output))
		if len(results) > 0 {
			_ = writeEvidenceFile(resultPath, []byte(strings.Join(results, "\n")+"\n"))
		}
	}
}

func collectResourceUsageEvidence(command, workspacePath string, stepMap stepMapFile) {
	resourceDir := filepath.Join(workspacePath, "evidence", "resources")
	if err := ensureSafeDirectory(resourceDir, 0o755); err != nil {
		return
	}
	scenarioSelector := "spex/owned=true,spex/scenario=" + stepMap.Metadata.Scenario
	scenarioArgs := kubectlArgsForWorkspace(workspacePath, stepMap.Spec.KubeContext, "-n", stepMap.Spec.Namespace, "top", "pod", "-l", scenarioSelector, "--containers")
	scenarioOutput, scenarioErr := runBoundedCommand(maxEvidenceResourceSize, command, scenarioArgs...)
	if len(scenarioOutput) > 0 {
		_ = writeEvidenceFile(filepath.Join(resourceDir, "scenario-pods.txt"), scenarioOutput)
	}
	if scenarioErr != nil {
		_ = writeEvidenceFile(filepath.Join(resourceDir, "scenario-pods.error.txt"), []byte(strings.TrimSpace(string(scenarioOutput))))
	}
	for _, step := range stepMap.Spec.Steps {
		ordinal := twoDigit(step.Ordinal)
		selector := podLogSelector(step)
		args := kubectlArgsForWorkspace(workspacePath, stepMap.Spec.KubeContext, "-n", stepMap.Spec.Namespace, "top", "pod", "-l", selector, "--containers")
		output, err := runBoundedCommand(maxEvidenceResourceSize, command, args...)
		if len(output) > 0 {
			_ = writeEvidenceFile(filepath.Join(resourceDir, ordinal+"-"+step.OperationID+".pods.txt"), output)
		}
		if err != nil {
			_ = writeEvidenceFile(filepath.Join(resourceDir, ordinal+"-"+step.OperationID+".pods.error.txt"), []byte(strings.TrimSpace(string(output))))
		}
	}
}

func runBoundedCommand(limit int64, command string, args ...string) ([]byte, error) {
	capture := newLimitedCapture(limit)
	cmd := exec.Command(command, args...)
	cmd.Stdout = capture
	cmd.Stderr = capture
	err := cmd.Run()
	return []byte(capture.String()), err
}

func writeEvidenceFile(path string, content []byte) error {
	return writeReportFile(path, content)
}

func podLogSelector(step stepMapStep) string {
	if len(step.PodSelector) == 0 {
		return "job-name=" + step.JobName
	}
	keys := make([]string, 0, len(step.PodSelector))
	for key := range step.PodSelector {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parts []string
	for _, key := range keys {
		parts = append(parts, key+"="+step.PodSelector[key])
	}
	return strings.Join(parts, ",")
}

func kubectlArgs(kubeContext string, args ...string) []string {
	if kubeContext == "" {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, "--context", kubeContext)
	out = append(out, args...)
	return out
}

func kubectlArgsForWorkspace(workspacePath, kubeContext string, args ...string) []string {
	kubeconfig := filepath.Join(workspacePath, "kubeconfig")
	if _, err := os.Stat(kubeconfig); err == nil {
		out := make([]string, 0, len(args)+2)
		out = append(out, "--kubeconfig", kubeconfig)
		out = append(out, args...)
		return out
	}
	return kubectlArgs(kubeContext, args...)
}

func probeResultLines(log string) []string {
	var results []string
	for _, line := range strings.Split(log, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, `"apiVersion":"spex.probe.result.v0.1"`) ||
			strings.Contains(trimmed, `"apiVersion": "spex.probe.result.v0.1"`) {
			results = append(results, trimmed)
		}
	}
	return results
}
