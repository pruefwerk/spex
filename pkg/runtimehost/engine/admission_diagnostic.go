package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type admissionReason string

const (
	identityMismatch         admissionReason = "IDENTITY_MISMATCH"
	runAttemptUnsupported    admissionReason = "RUN_ATTEMPT_UNSUPPORTED"
	policyRejected           admissionReason = "POLICY_REJECTED"
	policyUnavailable        admissionReason = "POLICY_UNAVAILABLE"
	repositoryMetadataFailed admissionReason = "REPOSITORY_METADATA_UNAVAILABLE"
	runMetadataFailed        admissionReason = "RUN_METADATA_UNAVAILABLE"
	workflowMetadataFailed   admissionReason = "WORKFLOW_METADATA_UNAVAILABLE"
	artifactMetadataFailed   admissionReason = "ARTIFACT_METADATA_UNAVAILABLE"
	artifactIdentityMismatch admissionReason = "ARTIFACT_IDENTITY_MISMATCH"
	artifactAgeInvalid       admissionReason = "ARTIFACT_AGE_INVALID"
	artifactDownloadFailed   admissionReason = "ARTIFACT_DOWNLOAD_FAILED"
	sourceDownloadFailed     admissionReason = "SOURCE_DOWNLOAD_FAILED"
	sourceExtractionFailed   admissionReason = "SOURCE_EXTRACTION_FAILED"
	preparationFailed        admissionReason = "PREPARATION_FAILED"
	runtimeIdentityInvalid   admissionReason = "RUNTIME_IDENTITY_INVALID"
)

// Keep causes available for errors.Is/As, but never include them in messages.
type admissionFailure struct {
	reason admissionReason
	cause  error
}

func admissionError(reason admissionReason, cause error) error {
	return &admissionFailure{reason: reason, cause: cause}
}

func (e *admissionFailure) Error() string {
	diagnostic := classifyAdmissionFailure(e)
	return fmt.Sprintf("admission failed: stage=%s reason=%s", diagnostic.Stage, diagnostic.Reason)
}

func (e *admissionFailure) Unwrap() error { return e.cause }

type admissionDiagnostic struct {
	Schema         string          `json:"schema"`
	Stage          string          `json:"stage"`
	Reason         admissionReason `json:"reason"`
	Classification string          `json:"classification"`
}

// The output boundary accepts only fixed stage/reason pairs. Unknown errors,
// including malformed typed diagnostics, must never become printable details.
func classifyAdmissionFailure(err error) admissionDiagnostic {
	diagnostic := admissionDiagnostic{
		Schema: "spex.admission-diagnostic/v1", Stage: "unknown",
		Reason: "ADMISSION_FAILED", Classification: "unknown_failure",
	}
	var failure *admissionFailure
	if !errors.As(err, &failure) || failure == nil {
		return diagnostic
	}
	switch failure.reason {
	case identityMismatch, runAttemptUnsupported, artifactIdentityMismatch, artifactAgeInvalid:
		diagnostic.Stage, diagnostic.Classification = "identity_validation", "admission_rejection"
	case policyRejected:
		diagnostic.Stage, diagnostic.Classification = "admission_policy", "intentional_refusal"
	case policyUnavailable:
		diagnostic.Stage, diagnostic.Classification = "admission_policy", "configuration_failure"
	case repositoryMetadataFailed, runMetadataFailed, workflowMetadataFailed, artifactMetadataFailed:
		diagnostic.Stage, diagnostic.Classification = "github_api", "external_operation_failure"
	case artifactDownloadFailed:
		diagnostic.Stage, diagnostic.Classification = "artifact_retrieval", "retrieval_failure"
	case sourceDownloadFailed:
		diagnostic.Stage, diagnostic.Classification = "source_retrieval", "retrieval_failure"
	case sourceExtractionFailed, preparationFailed, runtimeIdentityInvalid:
		diagnostic.Stage, diagnostic.Classification = "local_preparation", "local_execution_failure"
	default:
		return diagnostic
	}
	diagnostic.Reason = failure.reason
	return diagnostic
}

func writeAdmissionDiagnostic(destination io.Writer, err error) {
	fmt.Fprintln(destination, "Receiver source admission failed; execution did not start")
	_ = json.NewEncoder(destination).Encode(classifyAdmissionFailure(err))
}
