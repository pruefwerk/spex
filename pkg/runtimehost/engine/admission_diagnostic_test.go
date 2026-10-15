package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
)

const admissionSecret = "PRIVATE_ADMISSION_SENTINEL token=secret https://storage.invalid/archive?sig=secret response-body=secret"

func TestAdmissionDiagnosticsRedactCausesAndUnknownErrors(t *testing.T) {
	for _, tc := range []struct {
		reason                admissionReason
		stage, classification string
	}{
		{identityMismatch, "identity_validation", "admission_rejection"},
		{runAttemptUnsupported, "identity_validation", "admission_rejection"},
		{artifactIdentityMismatch, "identity_validation", "admission_rejection"},
		{artifactAgeInvalid, "identity_validation", "admission_rejection"},
		{policyRejected, "admission_policy", "intentional_refusal"},
		{policyUnavailable, "admission_policy", "configuration_failure"},
		{repositoryMetadataFailed, "github_api", "external_operation_failure"},
		{runMetadataFailed, "github_api", "external_operation_failure"},
		{workflowMetadataFailed, "github_api", "external_operation_failure"},
		{artifactMetadataFailed, "github_api", "external_operation_failure"},
		{artifactDownloadFailed, "artifact_retrieval", "retrieval_failure"},
		{sourceDownloadFailed, "source_retrieval", "retrieval_failure"},
		{sourceExtractionFailed, "local_preparation", "local_execution_failure"},
		{preparationFailed, "local_preparation", "local_execution_failure"},
		{runtimeIdentityInvalid, "local_preparation", "local_execution_failure"},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			cause := errors.New(admissionSecret)
			failure := admissionError(tc.reason, cause)
			if !errors.Is(failure, cause) {
				t.Fatal("underlying cause lost")
			}
			wrapped := fmt.Errorf("%s: %w", admissionSecret, failure)
			var output bytes.Buffer
			writeAdmissionDiagnostic(&output, wrapped)
			if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "PRIVATE") || strings.Contains(failure.Error(), "secret") {
				t.Fatal("admission diagnostic leaked a cause")
			}
			var diagnostic admissionDiagnostic
			_, line, _ := strings.Cut(output.String(), "\n")
			if err := json.Unmarshal([]byte(line), &diagnostic); err != nil {
				t.Fatal(err)
			}
			if diagnostic.Schema != "spex.admission-diagnostic/v1" || diagnostic.Stage != tc.stage || diagnostic.Reason != tc.reason || diagnostic.Classification != tc.classification {
				t.Fatal("incorrect diagnostic", diagnostic)
			}
		})
	}
	for _, err := range []error{errors.New(admissionSecret), admissionError(admissionReason(admissionSecret), errors.New(admissionSecret))} {
		var output bytes.Buffer
		writeAdmissionDiagnostic(&output, err)
		if strings.Contains(output.String(), "secret") || !strings.Contains(output.String(), `"reason":"ADMISSION_FAILED"`) || !strings.Contains(output.String(), `"stage":"unknown"`) {
			t.Fatal("unknown error did not use the safe fallback")
		}
	}
}

func sourceAdmissionFixture(t *testing.T) (*Host, string, map[string]string, *admissionAPI) {
	t.Helper()
	root, config := configuredFixture(t)
	// Admission makes the downloaded source read-only. Restore only this
	// fixture's directories before testing.TempDir removes them.
	t.Cleanup(func() {
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(path, 0700)
			}
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	h, err := New(config, "test")
	if err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(root, h.config.SourcePolicy)
	if err := os.MkdirAll(filepath.Dir(policy), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy, []byte(`{"schema":"spex.receiver-source-policy/v1","sources":[{"organization":"acme","visibilities":["internal"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	sha, id := strings.Repeat("a", 40), "0123456789abcdef0123456789abcdef"
	values := map[string]string{
		"TRANSPORT_SCHEMA": "spex.transport/v1", "GITHUB_RUN_ATTEMPT": "1", "REQUEST_ID": id,
		"REQUEST_SHA256": strings.Repeat("b", 64), "SOURCE_REPOSITORY": "acme/service", "SOURCE_RUN_ID": "12", "SOURCE_ARTIFACT_ID": "34",
		"GITHUB_REPOSITORY": "acme/service", "GITHUB_RUN_ID": "12", "GITHUB_SHA": sha, "GITHUB_EVENT_NAME": "push", "GITHUB_ACTOR": "developer",
	}
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	archive := tar.NewWriter(gz)
	// GitHub source archives carry Git's global commit metadata before files.
	if err := archive.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": sha}}); err != nil {
		t.Fatal(err)
	}
	if err := archive.WriteHeader(&tar.Header{Name: "repo/acceptance.feature", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	api := &admissionAPI{documents: map[string]string{
		"/repos/acme/service":                      `{"full_name":"acme/service","visibility":"internal","owner":{"login":"acme","type":"Organization"}}`,
		"/repos/acme/service/actions/runs/12":      `{"id":12,"run_attempt":1,"status":"in_progress","conclusion":null,"event":"push","repository":{"full_name":"acme/service"},"head_repository":{"full_name":"acme/service"},"head_sha":"` + sha + `","actor":{"login":"developer"},"workflow_id":7}`,
		"/repos/acme/service/actions/workflows/7":  `{"path":".github/workflows/acceptance.yml"}`,
		"/repos/acme/service/actions/artifacts/34": `{"id":34,"name":"spex-request-` + id + `","expired":false,"size_in_bytes":100,"created_at":"` + time.Now().UTC().Format(time.RFC3339) + `","workflow_run":{"id":12,"head_sha":"` + sha + `"}}`,
	}, failures: map[string]error{}, download: func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "/zip") {
			return []byte("request fixture"), nil
		}
		return data.Bytes(), nil
	}}
	return h, root, values, api
}

func TestSourceAdmissionFailureOriginsAndSuccess(t *testing.T) {
	for _, name := range []string{"success", "reusable success", "policy missing", "identity", "repository API", "run API", "workflow API", "artifact API", "artifact download", "source download", "source extraction", "private parent", "private directory", "artifact write", "worker run", "scope allocation", "scope directory", "scope save", "profile", "runtime identity", "context write", "outputs"} {
		t.Run(name, func(t *testing.T) {
			h, root, values, api := sourceAdmissionFixture(t)
			want := preparationFailed
			write := func(path string, data []byte) {
				t.Helper()
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			mkdir := func(path string) {
				t.Helper()
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			oldDownload := api.download
			switch name {
			case "success":
			case "reusable success":
				values["SPEX_ADMISSION_MODE"] = "reusable"
				mkdir(filepath.Join(root, ".spex"))
				write(filepath.Join(root, ".spex/runtime-release.json"), []byte(`{"release":"bundle-`+strings.Repeat("c", 64)+`"}`))
			case "policy missing":
				want = policyUnavailable
				if err := os.Remove(filepath.Join(root, h.config.SourcePolicy)); err != nil {
					t.Fatal(err)
				}
			case "identity":
				want = identityMismatch
				values["REQUEST_ID"] = "invalid"
			case "repository API", "run API", "workflow API", "artifact API":
				paths := map[string]string{"repository API": "/repos/acme/service", "run API": "/repos/acme/service/actions/runs/12", "workflow API": "/repos/acme/service/actions/workflows/7", "artifact API": "/repos/acme/service/actions/artifacts/34"}
				codes := map[string]admissionReason{"repository API": repositoryMetadataFailed, "run API": runMetadataFailed, "workflow API": workflowMetadataFailed, "artifact API": artifactMetadataFailed}
				api.failures[paths[name]] = fmt.Errorf("remote response: %w", errors.New(admissionSecret))
				want = codes[name]
			case "artifact download", "source download":
				want = artifactDownloadFailed
				if name == "source download" {
					want = sourceDownloadFailed
				}
				api.download = func(path string) ([]byte, error) {
					if strings.HasSuffix(path, "/zip") == (name == "artifact download") {
						return nil, errors.New(admissionSecret)
					}
					return oldDownload(path)
				}
			case "source extraction":
				want = sourceExtractionFailed
				api.download = func(path string) ([]byte, error) {
					if !strings.HasSuffix(path, "/zip") {
						return []byte(admissionSecret), nil
					}
					return oldDownload(path)
				}
			case "private directory":
				mkdir(filepath.Join(root, ".spex/receiver-private"))
			case "private parent":
				write(filepath.Join(root, ".spex"), []byte(admissionSecret))
			case "artifact write":
				api.download = func(path string) ([]byte, error) {
					mkdir(filepath.Join(root, ".spex/receiver-private/request.zip"))
					return oldDownload(path)
				}
			case "worker run":
				values["GITHUB_RUN_ID"] = "invalid"
			case "scope allocation":
				values["GITHUB_REPOSITORY"] = "invalid"
			case "scope directory", "scope save":
				api.download = func(path string) ([]byte, error) {
					if !strings.HasSuffix(path, "/zip") {
						scopes := filepath.Join(root, ".spex/execution-scopes")
						if name == "scope directory" {
							write(scopes, []byte(admissionSecret))
						} else {
							if os.Geteuid() == 0 {
								t.Skip("root bypasses directory write permissions")
							}
							mkdir(scopes)
							if err := os.Chmod(scopes, 0500); err != nil {
								t.Fatal(err)
							}
						}
					}
					return oldDownload(path)
				}
			case "profile":
				write(filepath.Join(root, h.config.Suite), []byte(admissionSecret))
			case "runtime identity":
				want = runtimeIdentityInvalid
				values["SPEX_ADMISSION_MODE"] = "reusable"
			case "context write":
				api.download = func(path string) ([]byte, error) {
					mkdir(filepath.Join(root, ".spex/receiver-private/context.json"))
					return oldDownload(path)
				}
			case "outputs":
				values["GITHUB_OUTPUT"] = root
			}
			err := h.admitSource(context.Background(), root, func(key string) string { return values[key] }, api)
			if name == "success" || name == "reusable success" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(root, ".spex/receiver-private/context.json")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil || classifyAdmissionFailure(err).Reason != want {
					t.Fatalf("got %v, want %s", err, want)
				}
				var logs bytes.Buffer
				writeAdmissionDiagnostic(&logs, err)
				if strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "PRIVATE") {
					t.Fatal("secret-bearing origin leaked")
				}
			}
			ledgers, err := filepath.Glob(filepath.Join(root, ".spex/isolated/*/creation.json"))
			if err != nil || len(ledgers) != 0 {
				t.Fatal("admission started an environment")
			}
		})
	}
}

func TestAdmissionCLIReportsTypedFailureWithoutStartingExecution(t *testing.T) {
	for _, operation := range []string{"source-admit", "source-admit-reusable"} {
		t.Run(operation, func(t *testing.T) {
			h, root, values, _ := sourceAdmissionFixture(t)
			t.Chdir(root)
			for key, value := range values {
				t.Setenv(key, value)
			}
			t.Setenv("GITHUB_RUN_ATTEMPT", "2")
			var out, logs bytes.Buffer
			if code := Run(context.Background(), []string{"--config", h.path, operation}, &out, &logs, "test"); code != 1 {
				t.Fatalf("exit code=%d", code)
			}
			if !strings.Contains(logs.String(), `"reason":"RUN_ATTEMPT_UNSUPPORTED"`) || out.Len() != 0 {
				t.Fatal("CLI lost diagnostic or reported success", logs.String())
			}
			if _, err := os.Stat(filepath.Join(root, ".spex/receiver-private")); !os.IsNotExist(err) {
				t.Fatal("rejected admission prepared execution")
			}
		})
	}
}

func TestAdmissionTransportAndArtifactDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, key, value, path, before, after string
		reason                                admissionReason
	}{
		{name: "schema", key: "TRANSPORT_SCHEMA", value: "invalid", reason: identityMismatch},
		{name: "run overflow", key: "SOURCE_RUN_ID", value: "9999999999999999999", reason: identityMismatch},
		{name: "artifact overflow", key: "SOURCE_ARTIFACT_ID", value: "9999999999999999999", reason: identityMismatch},
		{name: "source repository", key: "GITHUB_REPOSITORY", value: "acme/other", reason: identityMismatch},
		{name: "source run", key: "GITHUB_RUN_ID", value: "99", reason: identityMismatch},
		{name: "source sha", key: "GITHUB_SHA", value: strings.Repeat("c", 64), reason: identityMismatch},
		{name: "artifact name", path: "/repos/acme/service/actions/artifacts/34", before: "spex-request-", after: "other-", reason: artifactIdentityMismatch},
		{name: "artifact expiry missing", path: "/repos/acme/service/actions/artifacts/34", before: `"expired":false`, after: `"expired":null`, reason: artifactIdentityMismatch},
		{name: "artifact empty", path: "/repos/acme/service/actions/artifacts/34", before: `"size_in_bytes":100`, after: `"size_in_bytes":0`, reason: artifactIdentityMismatch},
		{name: "artifact too large", path: "/repos/acme/service/actions/artifacts/34", before: `"size_in_bytes":100`, after: `"size_in_bytes":8388609`, reason: artifactIdentityMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, values, api := sourceAdmissionFixture(t)
			values["SPEX_ADMISSION_MODE"] = "reusable"
			if tc.key != "" {
				values[tc.key] = tc.value
			}
			if tc.path != "" {
				api.documents[tc.path] = strings.Replace(api.documents[tc.path], tc.before, tc.after, 1)
			}
			_, err := h.authenticateSource(context.Background(), func(key string) string { return values[key] }, mustReadAdmissionPolicy(t, h), api, time.Now())
			if err == nil || classifyAdmissionFailure(err).Reason != tc.reason {
				t.Fatalf("got %v, want %s", err, tc.reason)
			}
		})
	}
}

func mustReadAdmissionPolicy(t *testing.T, h *Host) hostconfig.SourcePolicy {
	t.Helper()
	policy, err := h.resolver.ReadSourcePolicy(filepath.Join(filepath.Dir(h.path), h.config.SourcePolicy))
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestAdmissionUnavailableWorkingDirectoryIsClassifiedBeforeAPIAccess(t *testing.T) {
	h, root, values, api := sourceAdmissionFixture(t)
	directory := filepath.Join(root, "removed-working-directory")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(directory)
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	want := preparationFailed
	// Some systems can still resolve the removed cwd. In that case the next
	// operation fails while reading its policy, before any GitHub request.
	if _, err := filepath.Abs(""); err == nil {
		want = policyUnavailable
	}
	err := h.admitSource(context.Background(), "", func(key string) string { return values[key] }, api)
	if err == nil || classifyAdmissionFailure(err).Reason != want || len(api.calls) != 0 {
		t.Fatal("unavailable working directory lost classification or reached GitHub", err)
	}
}
