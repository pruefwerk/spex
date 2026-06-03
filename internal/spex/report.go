package spex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pruefwerk/spex/internal/workspace"
	"gopkg.in/yaml.v3"
)

const maxStepMapFileSize int64 = 1 << 20
const maxProbeResultFileSize int64 = 16 << 20
const maxJobStatusFileSize int64 = 1 << 20

type ReportInput struct {
	Workspace      string
	StartedAt      time.Time
	FinishedAt     time.Time
	ScenarioResult string
	RunnerResult   string
	FailureClass   *string
	FailureMessage *string
	KUTTLOutput    string
}

type ScenarioRunReport struct {
	APIVersion string         `yaml:"apiVersion" json:"apiVersion"`
	Kind       string         `yaml:"kind" json:"kind"`
	Metadata   ReportMetadata `yaml:"metadata" json:"metadata"`
	Status     ReportStatus   `yaml:"status" json:"status"`
	Spec       ReportSpec     `yaml:"spec" json:"spec"`
	Steps      []ReportStep   `yaml:"steps" json:"steps"`
}

type ReportMetadata struct {
	Name  string  `yaml:"name" json:"name"`
	RunID *string `yaml:"runId" json:"runId,omitempty"`
}

type ReportStatus struct {
	Result         string  `yaml:"result" json:"result"`
	ScenarioResult string  `yaml:"scenarioResult" json:"scenarioResult"`
	RunnerResult   string  `yaml:"runnerResult" json:"runnerResult"`
	StartedAt      string  `yaml:"startedAt" json:"startedAt"`
	FinishedAt     string  `yaml:"finishedAt" json:"finishedAt"`
	FailureClass   *string `yaml:"failureClass" json:"failureClass,omitempty"`
	FailureMessage *string `yaml:"failureMessage" json:"failureMessage,omitempty"`
}

type ReportSpec struct {
	Scenario     *string  `yaml:"scenario" json:"scenario,omitempty"`
	ScenarioFile *string  `yaml:"scenarioFile" json:"scenarioFile,omitempty"`
	BindingFile  *string  `yaml:"bindingFile" json:"bindingFile,omitempty"`
	CatalogFiles []string `yaml:"catalogFiles,omitempty" json:"catalogFiles,omitempty"`
	Namespace    *string  `yaml:"namespace" json:"namespace,omitempty"`
	Workspace    string   `yaml:"workspace" json:"workspace"`
}

type ReportStep struct {
	Ordinal        string   `yaml:"ordinal" json:"ordinal"`
	OperationID    string   `yaml:"operationId" json:"operationId"`
	OperationType  *string  `yaml:"operationType" json:"operationType,omitempty"`
	Internal       bool     `yaml:"internal,omitempty" json:"internal,omitempty"`
	Result         string   `yaml:"result" json:"result"`
	FailureClass   *string  `yaml:"failureClass" json:"failureClass,omitempty"`
	FailureMessage *string  `yaml:"failureMessage" json:"failureMessage,omitempty"`
	GeneratedFiles []string `yaml:"generatedFiles,omitempty" json:"generatedFiles,omitempty"`
	JobName        string   `yaml:"jobName" json:"jobName"`
	JobStatusRef   *string  `yaml:"jobStatusRef" json:"jobStatusRef,omitempty"`
	LogRef         *string  `yaml:"logRef" json:"logRef,omitempty"`
	ResultRef      *string  `yaml:"resultRef" json:"resultRef,omitempty"`
	ResourceRef    *string  `yaml:"resourceRef" json:"resourceRef,omitempty"`
}

type probeResult struct {
	APIVersion    string                  `json:"apiVersion"`
	Operation     string                  `json:"operation"`
	OperationID   string                  `json:"operationId"`
	OperationType string                  `json:"operationType"`
	Provider      string                  `json:"provider"`
	Status        string                  `json:"status"`
	FailureClass  string                  `json:"failureClass"`
	Reason        string                  `json:"reason"`
	Result        map[string]any          `json:"result"`
	Evidence      []probeEvidenceEnvelope `json:"evidence"`
	Diagnostics   []probeDiagnostic       `json:"diagnostics"`
}

type probeEvidenceEnvelope struct {
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"`
	Ref  string `json:"ref,omitempty"`
}

type probeDiagnostic struct {
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message"`
}

type jobStatus struct {
	Status struct {
		Conditions []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"conditions"`
	} `json:"status"`
}

type stepMapStep struct {
	Ordinal        int               `yaml:"ordinal"`
	OperationID    string            `yaml:"operationId"`
	OperationType  string            `yaml:"operationType"`
	JobName        string            `yaml:"jobName"`
	PodSelector    map[string]string `yaml:"podSelector"`
	GeneratedFiles []string          `yaml:"generatedFiles"`
}

type stepMapFile struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Scenario string `yaml:"scenario"`
		RunID    string `yaml:"runId"`
	} `yaml:"metadata"`
	Spec struct {
		ScenarioFile string        `yaml:"scenarioFile"`
		BindingFile  string        `yaml:"bindingFile"`
		Namespace    string        `yaml:"namespace"`
		KubeContext  string        `yaml:"kubeContext"`
		CatalogFiles []string      `yaml:"catalogFiles"`
		Steps        []stepMapStep `yaml:"steps"`
	} `yaml:"spec"`
}

func WriteReport(input ReportInput) (string, error) {
	stepMap, _ := loadStepMap(input.Workspace)
	probeResults := loadProbeResults(input.Workspace, stepMap)
	jobStatuses := loadJobStatuses(input.Workspace, stepMap)
	effectiveScenarioResult := effectiveScenarioResult(input, probeResults)
	effectiveInput := input
	effectiveInput.ScenarioResult = effectiveScenarioResult
	steps, mappedFailure := reportSteps(stepMap, effectiveInput, jobStatuses, probeResults)
	failureClass := input.FailureClass
	failureMessage := input.FailureMessage
	if input.RunnerResult == "passed" && effectiveScenarioResult == "failed" && !mappedFailure {
		class := "unmapped_kuttl_failure"
		message := strings.TrimSpace(input.KUTTLOutput)
		if message == "" {
			message = "KUTTL failed and no mapped generated Job could be identified"
		}
		failureClass = &class
		failureMessage = &message
	}
	report := ScenarioRunReport{
		APIVersion: "spex.report.v0.1",
		Kind:       "ScenarioRunReport",
		Metadata: ReportMetadata{
			Name:  reportName(stepMap),
			RunID: reportRunID(stepMap),
		},
		Status: ReportStatus{
			Result:         deriveResult(input.RunnerResult, effectiveScenarioResult),
			ScenarioResult: effectiveScenarioResult,
			RunnerResult:   input.RunnerResult,
			StartedAt:      input.StartedAt.Format(time.RFC3339),
			FinishedAt:     input.FinishedAt.Format(time.RFC3339),
			FailureClass:   failureClass,
			FailureMessage: failureMessage,
		},
		Spec: ReportSpec{
			Scenario:     nullableString(stepMap.Metadata.Scenario),
			ScenarioFile: nullableString(stepMap.Spec.ScenarioFile),
			BindingFile:  nullableString(stepMap.Spec.BindingFile),
			CatalogFiles: stepMap.Spec.CatalogFiles,
			Namespace:    nullableString(stepMap.Spec.Namespace),
			Workspace:    input.Workspace,
		},
		Steps: steps,
	}

	reportDir := filepath.Join(input.Workspace, "reports")
	if err := ensureSafeDirectory(reportDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(reportDir, "scenario-run-report.yaml")
	content, err := yaml.Marshal(report)
	if err != nil {
		return "", err
	}
	if err := writeReportFile(path, content); err != nil {
		return "", err
	}
	jsonPath := filepath.Join(reportDir, "scenario-run-report.json")
	jsonContent, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeReportFile(jsonPath, jsonContent); err != nil {
		return "", err
	}
	return path, nil
}

func writeReportFile(path string, content []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: not a regular file", filepath.Base(path))
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if err := syncDirectory(dir); err != nil {
		return err
	}
	keepTemp = true
	return nil
}

func ensureSafeDirectory(path string, mode os.FileMode) error {
	clean := filepath.Clean(path)
	if err := rejectUnsafeDirectory(clean); err != nil {
		return err
	}
	if err := os.MkdirAll(clean, mode); err != nil {
		return err
	}
	return rejectUnsafeDirectory(clean)
}

func ensureSafeDirectoryUnderRoot(root, path string, mode os.FileMode) error {
	cleanRoot := filepath.Clean(root)
	cleanPath := filepath.Clean(path)
	if err := rejectUnsafeDirectoryPathUnderRoot(cleanRoot, cleanPath); err != nil {
		return err
	}
	if err := os.MkdirAll(cleanPath, mode); err != nil {
		return err
	}
	if err := rejectUnsafeDirectoryPathUnderRoot(cleanRoot, cleanPath); err != nil {
		return err
	}
	return rejectUnsafeDirectory(cleanPath)
}

func rejectUnsafeDirectoryPathUnderRoot(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%s: path escapes root %s", path, root)
	}
	if err := rejectUnsafeDirectory(root); err != nil {
		return err
	}
	current := root
	if rel == "." {
		return nil
	}
	for _, elem := range strings.Split(rel, string(filepath.Separator)) {
		if elem == "" || elem == "." {
			continue
		}
		current = filepath.Join(current, elem)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: refusing symlink directory", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s: not a directory", current)
		}
	}
	return nil
}

func rejectUnsafeDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s: refusing symlink directory", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: not a directory", path)
	}
	return nil
}

func syncDirectory(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func loadStepMap(workspacePath string) (stepMapFile, error) {
	var out stepMapFile
	path := filepath.Join(workspacePath, "step-map.yaml")
	info, err := os.Lstat(path)
	if err != nil {
		return out, fmt.Errorf("step-map.yaml: %w", err)
	}
	if !info.Mode().IsRegular() {
		return out, fmt.Errorf("step-map.yaml: not a regular file")
	}
	if info.Size() > maxStepMapFileSize {
		return out, fmt.Errorf("step-map.yaml: file is too large: got %d bytes, max %d bytes", info.Size(), maxStepMapFileSize)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return out, fmt.Errorf("step-map.yaml: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	if err := decoder.Decode(&out); err != nil {
		return out, fmt.Errorf("step-map.yaml: %w", err)
	}
	if err := ensureYAMLEOF(decoder); err != nil {
		return out, fmt.Errorf("step-map.yaml: %w", err)
	}
	return out, nil
}

func reportName(stepMap stepMapFile) string {
	if stepMap.Metadata.Scenario == "" {
		return "unknown"
	}
	return stepMap.Metadata.Scenario
}

func reportRunID(stepMap stepMapFile) *string {
	return nullableString(stepMap.Metadata.RunID)
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func deriveResult(runnerResult, scenarioResult string) string {
	if runnerResult == "error" || scenarioResult == "error" {
		return "error"
	}
	if scenarioResult == "failed" {
		return "failed"
	}
	return "passed"
}

func reportSteps(stepMap stepMapFile, input ReportInput, jobStatuses map[string]jobStatus, probeResults map[string]probeResult) ([]ReportStep, bool) {
	steps := make([]ReportStep, 0, len(stepMap.Spec.Steps))
	failedIndex := failedStepIndexFromJobStatus(stepMap, jobStatuses)
	if failedIndex < 0 {
		failedIndex = failedStepIndexFromProbe(stepMap, probeResults)
	}
	if failedIndex < 0 {
		failedIndex = failedStepIndex(stepMap, input.KUTTLOutput)
	}
	for index, step := range stepMap.Spec.Steps {
		ordinal := twoDigit(step.Ordinal)
		jobStatusRef := filepath.ToSlash(filepath.Join("evidence", "status", ordinal+"-"+step.OperationID+".job.json"))
		logRef := filepath.ToSlash(filepath.Join("evidence", "logs", ordinal+"-"+step.OperationID+".log"))
		resultRef := filepath.ToSlash(filepath.Join("evidence", "results", ordinal+"-"+step.OperationID+".jsonl"))
		resourceRef := filepath.ToSlash(filepath.Join("evidence", "resources", ordinal+"-"+step.OperationID+".pods.txt"))
		existingJobStatusRef := existingEvidenceRef(input.Workspace, jobStatusRef)
		existingLogRef := existingEvidenceRef(input.Workspace, logRef)
		existingResultRef := existingEvidenceRef(input.Workspace, resultRef)
		existingResourceRef := existingEvidenceRef(input.Workspace, resourceRef)
		result := stepResult(input, failedIndex, index)
		var failureClass *string
		var failureMessage *string
		if result == "failed" {
			class, message := stepFailure(step, input.KUTTLOutput, existingLogRef != nil, jobStatuses, probeResults)
			failureClass = &class
			failureMessage = &message
		}
		steps = append(steps, ReportStep{
			Ordinal:        ordinal,
			OperationID:    step.OperationID,
			OperationType:  nullableString(step.OperationType),
			Internal:       step.OperationID == "redpanda-snapshot-offsets",
			Result:         result,
			FailureClass:   failureClass,
			FailureMessage: failureMessage,
			GeneratedFiles: step.GeneratedFiles,
			JobName:        step.JobName,
			JobStatusRef:   existingJobStatusRef,
			LogRef:         existingLogRef,
			ResultRef:      existingResultRef,
			ResourceRef:    existingResourceRef,
		})
	}
	return steps, failedIndex >= 0
}

func existingEvidenceRef(workspacePath, rel string) *string {
	info, err := os.Lstat(filepath.Join(workspacePath, filepath.FromSlash(rel)))
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	if maxSize := maxEvidenceRefSize(rel); maxSize > 0 && info.Size() > maxSize {
		return nil
	}
	return &rel
}

func maxEvidenceRefSize(rel string) int64 {
	switch {
	case strings.HasPrefix(rel, "evidence/results/"):
		return maxProbeResultFileSize
	case strings.HasPrefix(rel, "evidence/status/"):
		return maxJobStatusFileSize
	default:
		return 0
	}
}

func effectiveScenarioResult(input ReportInput, probeResults map[string]probeResult) string {
	if input.RunnerResult == "error" || input.ScenarioResult == "not_run" {
		return input.ScenarioResult
	}
	for _, result := range probeResults {
		if result.Status == "failed" || result.Status == "error" {
			return result.Status
		}
	}
	return input.ScenarioResult
}

func loadProbeResults(workspacePath string, stepMap stepMapFile) map[string]probeResult {
	results := map[string]probeResult{}
	for _, step := range stepMap.Spec.Steps {
		ordinal := twoDigit(step.Ordinal)
		path := filepath.Join(workspacePath, "evidence", "results", ordinal+"-"+step.OperationID+".jsonl")
		content, err := readRegularEvidenceFile(path, maxProbeResultFileSize)
		if err != nil {
			continue
		}
		var last probeResult
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var decoded probeResult
			if err := json.Unmarshal([]byte(line), &decoded); err != nil {
				continue
			}
			if decoded.APIVersion == "spex.probe.result.v0.1" {
				last = decoded
				continue
			}
			if validateNormalizedProbeResult(decoded, step) == nil {
				last = normalizeProbeResult(decoded)
			}
		}
		if last.APIVersion != "" || last.OperationID != "" {
			results[step.OperationID] = last
		}
	}
	return results
}

func validateNormalizedProbeResult(result probeResult, step stepMapStep) error {
	if result.APIVersion != "" {
		return fmt.Errorf("not a normalized probe envelope")
	}
	if result.OperationID == "" {
		return fmt.Errorf("normalized probe envelope missing operationId")
	}
	if result.OperationID != step.OperationID {
		return fmt.Errorf("normalized probe envelope operationId %q does not match step %q", result.OperationID, step.OperationID)
	}
	if result.OperationType == "" {
		return fmt.Errorf("normalized probe envelope missing operationType")
	}
	if result.Provider == "" {
		return fmt.Errorf("normalized probe envelope missing provider")
	}
	switch result.Status {
	case "passed", "failed", "error":
	default:
		return fmt.Errorf("normalized probe envelope status %q is unsupported", result.Status)
	}
	if result.Result == nil {
		return fmt.Errorf("normalized probe envelope missing result")
	}
	if result.Evidence == nil {
		return fmt.Errorf("normalized probe envelope missing evidence")
	}
	if result.Diagnostics == nil {
		return fmt.Errorf("normalized probe envelope missing diagnostics")
	}
	for i, diagnostic := range result.Diagnostics {
		if strings.TrimSpace(diagnostic.Message) == "" {
			return fmt.Errorf("normalized probe envelope diagnostics[%d].message is required", i)
		}
	}
	if result.Status == "passed" {
		if err := validateNormalizedProbeResultPayload(result); err != nil {
			return err
		}
	}
	return nil
}

func validateNormalizedProbeResultPayload(result probeResult) error {
	registry, err := workspace.NewBuiltInProviderRegistry()
	if err != nil {
		return err
	}
	capability, ok := registry.ResolveCapability(result.OperationType)
	if !ok {
		return nil
	}
	return workspace.ValidateCapabilityResult(result.OperationID, result.OperationType, capability.Capability, result.Result)
}

func normalizeProbeResult(result probeResult) probeResult {
	if result.Status == "failed" || result.Status == "error" {
		result.FailureClass = "probe_result_" + result.Status
		result.Reason = firstProbeDiagnosticMessage(result.Diagnostics)
		if result.Reason == "" {
			result.Reason = "probe reported " + result.Status
		}
	}
	return result
}

func firstProbeDiagnosticMessage(diagnostics []probeDiagnostic) string {
	for _, diagnostic := range diagnostics {
		if strings.TrimSpace(diagnostic.Message) != "" {
			return diagnostic.Message
		}
	}
	return ""
}

func loadJobStatuses(workspacePath string, stepMap stepMapFile) map[string]jobStatus {
	statuses := map[string]jobStatus{}
	for _, step := range stepMap.Spec.Steps {
		ordinal := twoDigit(step.Ordinal)
		path := filepath.Join(workspacePath, "evidence", "status", ordinal+"-"+step.OperationID+".job.json")
		content, err := readRegularEvidenceFile(path, maxJobStatusFileSize)
		if err != nil {
			continue
		}
		var decoded jobStatus
		if err := json.Unmarshal(content, &decoded); err != nil {
			continue
		}
		statuses[step.OperationID] = decoded
	}
	return statuses
}

func readRegularEvidenceFile(path string, maxSize int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", filepath.Base(path))
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("%s: file is too large: got %d bytes, max %d bytes", filepath.Base(path), info.Size(), maxSize)
	}
	return os.ReadFile(path)
}

func failedStepIndexFromJobStatus(stepMap stepMapFile, jobStatuses map[string]jobStatus) int {
	for index, step := range stepMap.Spec.Steps {
		status, ok := jobStatuses[step.OperationID]
		if !ok {
			continue
		}
		if jobFailed(status) {
			return index
		}
	}
	return -1
}

func jobFailed(status jobStatus) bool {
	for _, condition := range status.Status.Conditions {
		if condition.Type == "Failed" && condition.Status == "True" {
			return true
		}
	}
	return false
}

func failedStepIndexFromProbe(stepMap stepMapFile, probeResults map[string]probeResult) int {
	for index, step := range stepMap.Spec.Steps {
		result, ok := probeResults[step.OperationID]
		if !ok {
			continue
		}
		if result.Status == "failed" || result.Status == "error" {
			return index
		}
	}
	return -1
}

func failedStepIndex(stepMap stepMapFile, output string) int {
	if output == "" {
		return -1
	}
	for index, step := range stepMap.Spec.Steps {
		if step.JobName != "" && strings.Contains(output, step.JobName) {
			return index
		}
		if step.OperationID != "" && strings.Contains(output, step.OperationID) {
			return index
		}
		for _, generatedFile := range step.GeneratedFiles {
			if generatedFile != "" && strings.Contains(output, generatedFile) {
				return index
			}
			base := filepath.Base(generatedFile)
			if base != "" && strings.Contains(output, base) {
				return index
			}
		}
	}
	return -1
}

func stepResult(input ReportInput, failedIndex, index int) string {
	if input.RunnerResult == "error" && input.ScenarioResult == "not_run" {
		return "not_run"
	}
	if input.ScenarioResult == "failed" {
		if failedIndex < 0 {
			return "not_run"
		}
		if index < failedIndex {
			return "passed"
		}
		if index == failedIndex {
			return "failed"
		}
		return "skipped"
	}
	return "passed"
}

func stepFailure(step stepMapStep, output string, logCollected bool, jobStatuses map[string]jobStatus, probeResults map[string]probeResult) (string, string) {
	if status, ok := jobStatuses[step.OperationID]; ok && jobFailed(status) {
		return "probe_job_failed", jobFailureMessage(status)
	}
	if result, ok := probeResults[step.OperationID]; ok && (result.Status == "failed" || result.Status == "error") {
		class := result.FailureClass
		if class == "" {
			class = "probe_result_" + result.Status
		}
		message := result.Reason
		if message == "" {
			message = "probe reported " + result.Status
		}
		return class, message
	}
	if !logCollected && outputMentionsJob(step, output) {
		return "pod_log_collection_missing_pod", "KUTTL reported mapped Job failure but no Pod log could be collected for " + step.JobName
	}
	return "kuttl_execution_failure", failureMessageForStep(step, output)
}

func outputMentionsJob(step stepMapStep, output string) bool {
	return step.JobName != "" && strings.Contains(output, step.JobName)
}

func jobFailureMessage(status jobStatus) string {
	for _, condition := range status.Status.Conditions {
		if condition.Type == "Failed" && condition.Status == "True" {
			message := strings.TrimSpace(condition.Message)
			if message != "" {
				return message
			}
			reason := strings.TrimSpace(condition.Reason)
			if reason != "" {
				return reason
			}
			return "Kubernetes Job reported Failed=True"
		}
	}
	return "Kubernetes Job reported failure"
}

func failureMessageForStep(step stepMapStep, output string) string {
	if output == "" {
		return "KUTTL reported failure for " + step.JobName
	}
	for _, line := range strings.Split(output, "\n") {
		if step.JobName != "" && strings.Contains(line, step.JobName) {
			return strings.TrimSpace(line)
		}
		for _, generatedFile := range step.GeneratedFiles {
			if generatedFile != "" && strings.Contains(line, generatedFile) {
				return strings.TrimSpace(line)
			}
			base := filepath.Base(generatedFile)
			if base != "" && strings.Contains(line, base) {
				return strings.TrimSpace(line)
			}
		}
	}
	return strings.TrimSpace(output)
}

func twoDigit(value int) string {
	if value < 10 {
		return "0" + string(rune('0'+value))
	}
	return strconv.Itoa(value)
}
