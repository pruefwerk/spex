package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateResponseComparesJSONSemantically(t *testing.T) {
	t.Parallel()
	op := operation{
		ExpectedBody:   `{"known":true,"migrated":false}`,
		ExpectedStatus: http.StatusOK,
	}
	if err := validateResponse(
		op,
		http.StatusOK,
		`{"migrated":false,"known":true}`,
	); err != nil {
		t.Fatalf("validateResponse() error = %v", err)
	}
}

func TestPostJSONRetriesUntilExpectationMatches(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		count := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"%s"}`, map[bool]string{
			true:  "confirmed",
			false: "pending",
		}[count >= 2])
	}))
	t.Cleanup(server.Close)
	t.Setenv("SPEX_HTTP_ENDPOINT", server.URL)

	status, body, err := postJSON(context.Background(), operation{
		Method:         http.MethodGet,
		Path:           "/migration",
		ExpectedStatus: http.StatusOK,
		ExpectedBody:   `{"status":"confirmed"}`,
	}, time.Second)
	if err != nil {
		t.Fatalf("postJSON() error = %v", err)
	}
	if status != http.StatusOK || body != `{"status":"confirmed"}` {
		t.Fatalf("postJSON() = (%d, %q), want (200, confirmed body)", status, body)
	}
	if requests.Load() < 2 {
		t.Fatalf("request count = %d, want at least 2", requests.Load())
	}
}

func TestWritesDoNotRetryFailuresOrMismatchedResponses(t *testing.T) {
	for _, verb := range []string{"POST", "PUT", "DELETE"} {
		for _, status := range []int{200, 503} {
			t.Run(fmt.Sprintf("%s/%d", verb, status), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.WriteHeader(status)
					fmt.Fprint(w, `{"state":"pending"}`)
				}))
				t.Cleanup(server.Close)
				t.Setenv("SPEX_HTTP_ENDPOINT", server.URL)
				op := operation{Method: verb, Path: "/command", ExpectedStatus: 200, ExpectedBody: `{"state":"done"}`}
				if _, _, err := postJSON(context.Background(), op, time.Second); err == nil {
					t.Fatal("unmatched response passed")
				}
				if requests.Load() != 1 {
					t.Fatalf("write sent %d times, want once", requests.Load())
				}
			})
		}
	}
}

func TestAuthenticatedPUTUsesTheBoundOperatorToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/gateways/0200000100001007/aws-native" || r.Header.Get("Authorization") != "Bearer kind-test-operator" {
			http.Error(w, "wrong method, path or authorization", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"gatewayId":"0200000100001007","provisioningId":"install-1","registeredAt":"2026-10-01T00:00:00Z"}`)
	}))
	t.Cleanup(server.Close)
	t.Setenv("SPEX_HTTP_ENDPOINT", server.URL)
	t.Setenv("SPEX_HTTP_BEARER_TOKEN", "kind-test-operator")
	_, _, err := postJSON(context.Background(), operation{
		Method: http.MethodPut, Path: "/api/v1/gateways/0200000100001007/aws-native",
		Body: `{"provisioningId":"install-1"}`, ExpectedStatus: http.StatusOK,
		ExpectedFields: map[string]any{"/gatewayId": "0200000100001007", "/provisioningId": "install-1"},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRequestURLPreservesQuery(t *testing.T) {
	t.Parallel()
	target, err := requestURL(
		"http://service.example:8080",
		"/control/metric-checks/gateway?stableFor=11s",
	)
	if err != nil {
		t.Fatalf("requestURL() error = %v", err)
	}
	const want = "http://service.example:8080/control/metric-checks/gateway?stableFor=11s"
	if target != want {
		t.Fatalf("requestURL() = %q, want %q", target, want)
	}
}

func TestValidateResponseFields(t *testing.T) {
	length := 2
	op := operation{
		ExpectedStatus:      http.StatusOK,
		ExpectedArrayLength: &length,
		ExpectedFields: map[string]any{
			"/0/spotId": "recent", "/0/state": "ok",
			"/1/spotId": "missing", "/1/state": "unknown",
		},
	}
	for _, tc := range []struct {
		name      string
		body      string
		wantError bool
	}{
		{"dynamic timestamp", `[{"spotId":"recent","state":"ok","lastSeen":"now"},{"spotId":"missing","state":"unknown"}]`, false},
		{"wrong state", `[{"spotId":"recent","state":"critical"},{"spotId":"missing","state":"unknown"}]`, true},
		{"wrong spot", `[{"spotId":"other","state":"ok"},{"spotId":"missing","state":"unknown"}]`, true},
		{"missing field", `[{"spotId":"recent"},{"spotId":"missing","state":"unknown"}]`, true},
		{"extra entry", `[{"spotId":"recent","state":"ok"},{"spotId":"missing","state":"unknown"},{}]`, true},
		{"not an array", `{}`, true},
		{"invalid JSON", `not JSON`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateResponse(op, http.StatusOK, tc.body)
			if (err != nil) != tc.wantError {
				t.Fatalf("validateResponse() = %v, wantError %v", err, tc.wantError)
			}
		})
	}
	if err := validateResponse(op, http.StatusInternalServerError, `[]`); err == nil {
		t.Fatal("wrong HTTP status passed validation")
	}
}

func TestJSONPointer(t *testing.T) {
	value := map[string]any{"a/b": map[string]any{"~key": []any{nil, "ok"}}}
	for _, pointer := range []string{"/a~1b/~0key/1", "/a~1b/~0key/0"} {
		if _, err := jsonPointer(value, pointer); err != nil {
			t.Fatalf("jsonPointer(%q): %v", pointer, err)
		}
	}
	for _, pointer := range []string{"a", "/absent", "/a~2b", "/a~1b/~0key/01", "/a~1b/~0key/-1", "/a~1b/~0key/9", "/a~1b/~0key/0/x"} {
		if _, err := jsonPointer(value, pointer); err == nil {
			t.Errorf("jsonPointer(%q) unexpectedly passed", pointer)
		}
	}
}

func TestReadOperationFieldExpectations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.json")
	data := `{"operationId":"health","with":{"method":"POST","path":"/api/v1/health/query","expectedStatus":200,"expectedArrayLength":1,"expectedFields":{"/0/state":"ok"}}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	op, err := readOperation(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateResponse(op, 200, `[{"state":"ok","lastSeen":"variable"}]`); err != nil {
		t.Fatal(err)
	}
	if err := validateResponse(op, 200, `[{"state":"unknown"}]`); err == nil {
		t.Fatal("readOperation lost field expectations")
	}
}

func TestDynamicRetryBodyIsResolvedOnceAndRetransmittedUnchanged(t *testing.T) {
	var reads, writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer kind-test-operator" {
			t.Error("bound token missing")
		}
		if r.Method == http.MethodGet && r.URL.Path == "/operations" {
			if reads.Add(1) > 1 {
				t.Error("retransmission refreshed the expected generation")
			}
			fmt.Fprint(w, `{"intent":{"startedAt":"2026-10-04T00:00:00Z"}}`)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/retry" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["expectedStartedAt"] != "2026-10-04T00:00:00Z" || body["requestId"] != "same-request" || body["reason"] != "repaired" {
			t.Errorf("retry body changed: %#v (%v)", body, err)
		}
		if writes.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"requestId":"same-request"}`)
	}))
	t.Cleanup(server.Close)
	t.Setenv("SPEX_HTTP_ENDPOINT", server.URL)
	t.Setenv("SPEX_HTTP_BEARER_TOKEN", "kind-test-operator")
	op := operation{Method: "POST", RetryWrites: true, Path: "/retry", ExpectedStatus: 202,
		Body:           `{"requestId":"same-request","reason":"repaired","expectedStartedAt":null}`,
		BodyFieldsFrom: map[string]jsonFieldSource{"expectedStartedAt": {Path: "/operations", Pointer: "/intent/startedAt"}}}
	if _, _, err := postJSON(context.Background(), op, time.Second); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 1 || writes.Load() != 2 {
		t.Fatalf("reads=%d writes=%d", reads.Load(), writes.Load())
	}
}

func TestRetryReplayUsesTheOriginalReceiptTimestamp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"intent":{"startedAt":"2026-10-04T00:02:00Z"},"retries":[{"expectedStartedAt":"2026-10-04T00:00:00Z"}]}`)
	}))
	t.Cleanup(server.Close)
	op := operation{Body: `{"expectedStartedAt":null}`, BodyFieldsFrom: map[string]jsonFieldSource{
		"expectedStartedAt": {Path: "/operations", Pointer: "/retries/0/expectedStartedAt"},
	}}
	body, err := resolveBodyFields(context.Background(), server.URL, op)
	if err != nil || body != `{"expectedStartedAt":"2026-10-04T00:00:00Z"}` {
		t.Fatalf("body=%s err=%v", body, err)
	}
}

func TestDynamicBodyFailsClosedBeforeAnyMutation(t *testing.T) {
	for _, tc := range []struct {
		name, body, sourcePath, sourceJSON, pointer string
		status                                      int
	}{
		{"missing timestamp", `{"expectedStartedAt":null}`, "/operations", `{}`, "/intent/startedAt", 200},
		{"null timestamp", `{"expectedStartedAt":null}`, "/operations", `{"intent":{"startedAt":null}}`, "/intent/startedAt", 200},
		{"malformed source", `{"expectedStartedAt":null}`, "/operations", `bad`, "/intent/startedAt", 200},
		{"failed read", `{"expectedStartedAt":null}`, "/operations", `{}`, "/intent/startedAt", 503},
		{"absolute source", `{"expectedStartedAt":null}`, "https://other.invalid/operations", `{}`, "/intent/startedAt", 200},
		{"empty source", `{"expectedStartedAt":null}`, "", `{}`, "/intent/startedAt", 200},
		{"non-object body", `[]`, "/operations", `{}`, "/intent/startedAt", 200},
		{"absent request field", `{}`, "/operations", `{}`, "/intent/startedAt", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mutations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mutations.Add(1)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.sourceJSON)
			}))
			t.Cleanup(server.Close)
			t.Setenv("SPEX_HTTP_ENDPOINT", server.URL)
			op := operation{Method: "POST", Path: "/retry", Body: tc.body, ExpectedStatus: 202,
				BodyFieldsFrom: map[string]jsonFieldSource{"expectedStartedAt": {Path: tc.sourcePath, Pointer: tc.pointer}}}
			if _, _, err := postJSON(context.Background(), op, time.Second); err == nil || mutations.Load() != 0 {
				t.Fatalf("err=%v mutations=%d", err, mutations.Load())
			}
		})
	}
}

func TestDynamicBodyDoesNotFollowRedirectsWithOperatorCredentials(t *testing.T) {
	var redirected atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	t.Cleanup(other.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	t.Cleanup(server.Close)
	t.Setenv("SPEX_HTTP_BEARER_TOKEN", "kind-test-operator")
	op := operation{Body: `{"expectedStartedAt":null}`, BodyFieldsFrom: map[string]jsonFieldSource{
		"expectedStartedAt": {Path: "/operations", Pointer: "/intent/startedAt"},
	}}
	if _, err := resolveBodyFields(context.Background(), server.URL, op); err == nil || redirected.Load() != 0 {
		t.Fatalf("err=%v redirected=%d", err, redirected.Load())
	}
}

func TestLoweredDynamicBodyFieldsReachTheProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.json")
	data := `{"with":{"method":"POST","path":"/retry","body":"{\"expectedStartedAt\":null}","bodyFieldsFrom":{"expectedStartedAt":{"path":"/operations","pointer":"/intent/startedAt"}},"expectedStatus":202}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	op, err := readOperation(path)
	if err != nil || op.BodyFieldsFrom["expectedStartedAt"].Pointer != "/intent/startedAt" {
		t.Fatalf("dynamic fields lost: %#v %v", op, err)
	}
	// Existing operations still send their literal body unchanged.
	if body, err := resolveBodyFields(context.Background(), "", operation{Body: "literal"}); err != nil || body != "literal" {
		t.Fatalf("legacy body=%q err=%v", body, err)
	}
}

func TestReplayMustReturnTheEntireOriginalReceipt(t *testing.T) {
	const receipt = `{"requestId":"retry-one","expectedStartedAt":"2026-10-04T00:00:00Z","startedAt":"2026-10-04T00:02:00Z","deadlineAt":"2026-10-04T00:04:00Z"}`
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%t", changed), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprintf(w, `{"retries":[%s]}`, receipt)
					return
				}
				w.WriteHeader(202)
				if changed {
					fmt.Fprint(w, `{"requestId":"retry-one","expectedStartedAt":"2026-10-04T00:00:00Z","startedAt":"2026-10-04T00:03:00Z","deadlineAt":"2026-10-04T00:05:00Z"}`)
				} else {
					fmt.Fprint(w, receipt)
				}
			}))
			t.Cleanup(server.Close)
			t.Setenv("SPEX_HTTP_ENDPOINT", server.URL)
			op := operation{Method: "POST", Path: "/retry", ExpectedStatus: 202,
				Body:             `{"requestId":"retry-one","expectedStartedAt":null}`,
				BodyFieldsFrom:   map[string]jsonFieldSource{"expectedStartedAt": {Path: "/operations", Pointer: "/retries/0/expectedStartedAt"}},
				ExpectedBodyFrom: &jsonFieldSource{Path: "/operations", Pointer: "/retries/0"}}
			if _, _, err := postJSON(context.Background(), op, 600*time.Millisecond); (err != nil) != changed {
				t.Fatalf("err=%v changed=%t", err, changed)
			}
		})
	}
}
