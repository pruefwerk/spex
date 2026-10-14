package spex

import (
	"strings"
)

// RunnerDiagnostic contains only fixed vocabulary and numeric observations.
// Never add command text, backend responses, or paths to this exported evidence.
type RunnerDiagnostic struct {
	Phase              string `json:"phase" yaml:"phase"`
	Reason             string `json:"reason" yaml:"reason"`
	LastProfileCommand int    `json:"last_profile_command,omitempty" yaml:"lastProfileCommand,omitempty"`
	ExitCode           *int   `json:"exit_code,omitempty" yaml:"exitCode,omitempty"`
}

func diagnoseRunner(output string, exitCode *int) *RunnerDiagnostic {
	d := &RunnerDiagnostic{Phase: "harness_setup", Reason: "kuttl_failed", ExitCode: exitCode}
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "|  | running command:") && d.LastProfileCommand < 10000 {
			d.LastProfileCommand++
			d.Phase = "profile_setup"
		}
		if strings.Contains(line, "| starting test step ") {
			switch {
			case strings.Contains(line, "-integration-setup"):
				d.Phase = "integration_setup"
			case strings.Contains(line, "-rerun-cleanup"):
				d.Phase = "rerun_cleanup"
			case strings.Contains(line, "-rbac"):
				d.Phase = "static_setup"
			default:
				d.Phase = "test_execution"
			}
		}
	}
	// These are hints from captured runner output, not proof of root cause.
	// Copy only a fixed code, never a matching line or a captured substring.
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, `unknown command "kuttl"`):
		d.Reason = "kuttl_plugin_unavailable"
	case strings.Contains(lower, "no configuration has been provided"):
		d.Reason = "kubeconfig_unavailable"
	case strings.Contains(lower, "context") && strings.Contains(lower, "does not exist"):
		d.Reason = "kube_context_unavailable"
	case strings.Contains(lower, "fatal error getting client"):
		d.Reason = "kubernetes_client_failed"
	case strings.Contains(lower, "connection refused"):
		d.Reason = "connection_refused"
	case strings.Contains(lower, "permission denied"):
		d.Reason = "permission_denied"
	case strings.Contains(lower, "context deadline exceeded"):
		d.Reason = "deadline_exceeded"
	}
	return safeRunnerDiagnostic(d)
}

// Revalidate persisted reports before including them in portable artifacts.
func safeRunnerDiagnostic(d *RunnerDiagnostic) *RunnerDiagnostic {
	if d == nil {
		return nil
	}
	copy := &RunnerDiagnostic{}
	switch d.Phase {
	case "harness_setup", "profile_setup", "integration_setup", "rerun_cleanup", "static_setup", "test_execution":
		copy.Phase = d.Phase
	default:
		return nil
	}
	switch d.Reason {
	case "kuttl_failed", "kuttl_plugin_unavailable", "kubeconfig_unavailable", "kube_context_unavailable", "kubernetes_client_failed", "connection_refused", "permission_denied", "deadline_exceeded":
		copy.Reason = d.Reason
	default:
		copy.Reason = "kuttl_failed"
	}
	if d.LastProfileCommand > 0 && d.LastProfileCommand <= 10000 {
		copy.LastProfileCommand = d.LastProfileCommand
	}
	if d.ExitCode != nil && *d.ExitCode >= 0 && *d.ExitCode <= 255 {
		code := *d.ExitCode
		copy.ExitCode = &code
	}
	return copy
}
