package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

type operation struct {
	OperationID         string                     `json:"operationId"`
	OperationType       string                     `json:"operationType"`
	Provider            string                     `json:"provider"`
	With                *operationInput            `json:"with"`
	Path                string                     `json:"path"`
	Method              string                     `json:"method"`
	RetryWrites         bool                       `json:"retryWrites"`
	ContentType         string                     `json:"contentType"`
	Body                string                     `json:"body"`
	ExpectedBody        string                     `json:"expectedBody"`
	ExpectedFields      map[string]any             `json:"expectedFields"`
	ExpectedArrayLength *int                       `json:"expectedArrayLength"`
	ExpectedStatus      int                        `json:"expectedStatus"`
	Headers             map[string]string          `json:"headers"`
	BodyFieldsFrom      map[string]jsonFieldSource `json:"bodyFieldsFrom"`
	ExpectedBodyFrom    *jsonFieldSource           `json:"expectedBodyFrom"`
}

type operationInput struct {
	Path                string                     `json:"path"`
	Method              string                     `json:"method"`
	RetryWrites         bool                       `json:"retryWrites"`
	ContentType         string                     `json:"contentType"`
	Body                string                     `json:"body"`
	ExpectedBody        string                     `json:"expectedBody"`
	ExpectedFields      map[string]any             `json:"expectedFields"`
	ExpectedArrayLength *int                       `json:"expectedArrayLength"`
	ExpectedStatus      int                        `json:"expectedStatus"`
	Headers             map[string]string          `json:"headers"`
	BodyFieldsFrom      map[string]jsonFieldSource `json:"bodyFieldsFrom"`
	ExpectedBodyFrom    *jsonFieldSource           `json:"expectedBodyFrom"`
}

// Resolve dynamic request fields through read-only calls to the same binding.
// Resolve once, before retransmission: retries must keep their original input.
type jsonFieldSource struct {
	Path    string `json:"path"`
	Pointer string `json:"pointer"`
}

type resultEnvelope struct {
	OperationID   string         `json:"operationId"`
	OperationType string         `json:"operationType"`
	Provider      string         `json:"provider,omitempty"`
	Status        string         `json:"status"`
	Result        map[string]any `json:"result,omitempty"`
	Evidence      []evidence     `json:"evidence"`
	Diagnostics   []diagnostic   `json:"diagnostics"`
}

type evidence struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref,omitempty"`
}

type diagnostic struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "run" {
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("http-probe", flag.ContinueOnError)
	operationFile := fs.String("operation-file", "", "lowered operation JSON file")
	resultFile := fs.String("result-file", "", "probe result JSON file")
	timeoutValue := fs.String("timeout", "30s", "operation timeout")
	pollValue := fs.String("poll-interval", "250ms", "interval between read polls or explicitly enabled write retries")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *operationFile == "" || *resultFile == "" {
		return errors.New("--operation-file and --result-file are required")
	}
	timeout, err := time.ParseDuration(*timeoutValue)
	if err != nil {
		return fmt.Errorf("parse timeout: %w", err)
	}
	pollInterval, err := time.ParseDuration(*pollValue)
	if err != nil || pollInterval <= 0 {
		return errors.New("--poll-interval must be a positive duration")
	}
	op, err := readOperation(*operationFile)
	if err != nil {
		return err
	}
	status, body, err := postJSON(context.Background(), op, timeout, pollInterval)
	result := resultEnvelope{
		OperationID:   op.OperationID,
		OperationType: op.OperationType,
		Provider:      op.Provider,
		Status:        "passed",
		Result: map[string]any{
			"statusCode": status,
			"body":       body,
		},
		Evidence:    []evidence{{Kind: "http", Ref: fmt.Sprintf("%s %s returned HTTP %d", method(op), op.Path, status)}},
		Diagnostics: []diagnostic{},
	}
	if err != nil {
		result.Status = "failed"
		result.Diagnostics = []diagnostic{{Severity: "error", Message: err.Error()}}
	}
	if writeErr := writeResult(*resultFile, result); writeErr != nil {
		return writeErr
	}
	return err
}

func readOperation(path string) (operation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return operation{}, fmt.Errorf("read operation file: %w", err)
	}
	var op operation
	if err := json.Unmarshal(data, &op); err != nil {
		return operation{}, fmt.Errorf("decode operation file: %w", err)
	}
	if op.With != nil {
		op.Path = op.With.Path
		op.Method = op.With.Method
		op.RetryWrites = op.With.RetryWrites
		op.ContentType = op.With.ContentType
		op.Body = op.With.Body
		op.ExpectedBody = op.With.ExpectedBody
		op.ExpectedFields = op.With.ExpectedFields
		op.ExpectedArrayLength = op.With.ExpectedArrayLength
		op.ExpectedStatus = op.With.ExpectedStatus
		op.Headers = op.With.Headers
		op.BodyFieldsFrom = op.With.BodyFieldsFrom
		op.ExpectedBodyFrom = op.With.ExpectedBodyFrom
	}
	if op.Path == "" {
		return operation{}, errors.New("operation path is required")
	}
	if op.ExpectedStatus == 0 {
		op.ExpectedStatus = http.StatusOK
	}
	return op, nil
}

func postJSON(ctx context.Context, op operation, timeout time.Duration, intervals ...time.Duration) (int, string, error) {
	interval := 250 * time.Millisecond
	if len(intervals) > 0 {
		interval = intervals[0]
	}
	if interval <= 0 {
		return 0, "", errors.New("poll interval must be positive")
	}
	endpoint := strings.TrimRight(os.Getenv("SPEX_HTTP_ENDPOINT"), "/")
	if endpoint == "" {
		return 0, "", errors.New("SPEX_HTTP_ENDPOINT is required")
	}
	target, err := requestURL(endpoint, op.Path)
	if err != nil {
		return 0, "", fmt.Errorf("build request URL: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	op.Body, err = resolveBodyFields(ctx, endpoint, op)
	if err != nil {
		return 0, "", err
	}
	if op.ExpectedBodyFrom != nil {
		value, sourceErr := readJSONSource(ctx, endpoint, op.ExpectedBodyFrom.Path)
		if sourceErr != nil {
			return 0, "", sourceErr
		}
		value, sourceErr = jsonPointer(value, op.ExpectedBodyFrom.Pointer)
		if sourceErr != nil || value == nil {
			return 0, "", errors.New("expected body source missing or null")
		}
		data, sourceErr := json.Marshal(value)
		if sourceErr != nil {
			return 0, "", sourceErr
		}
		op.ExpectedBody = string(data)
	}

	var lastStatus int
	var lastBody string
	var lastErr error
	for {
		lastStatus, lastBody, lastErr = requestJSON(ctx, op, target)
		if lastErr == nil {
			lastErr = validateResponse(op, lastStatus, lastBody)
		}
		if lastErr == nil {
			return lastStatus, lastBody, nil
		}
		// Poll reads, but send commands once unless the scenario explicitly tests
		// retransmission. A failed assertion must not silently repeat a mutation.
		if method(op) != http.MethodGet && !op.RetryWrites {
			return lastStatus, lastBody, lastErr
		}
		select {
		case <-ctx.Done():
			return lastStatus, lastBody, fmt.Errorf(
				"HTTP expectation not met before timeout: %w",
				lastErr,
			)
		case <-time.After(interval):
		}
	}
}

func resolveBodyFields(ctx context.Context, endpoint string, op operation) (string, error) {
	if len(op.BodyFieldsFrom) == 0 {
		return op.Body, nil
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(op.Body), &body); err != nil || body == nil {
		return "", errors.New("bodyFieldsFrom requires a JSON object body")
	}
	fields := make([]string, 0, len(op.BodyFieldsFrom))
	for field := range op.BodyFieldsFrom {
		if _, exists := body[field]; !exists {
			return "", fmt.Errorf("request body has no field %q", field)
		}
		fields = append(fields, field)
	}
	sort.Strings(fields)
	sources := make(map[string]any)
	for _, field := range fields {
		source := op.BodyFieldsFrom[field]
		value, exists := sources[source.Path]
		if !exists {
			var err error
			value, err = readJSONSource(ctx, endpoint, source.Path)
			if err != nil {
				return "", err
			}
			sources[source.Path] = value
		}
		resolved, err := jsonPointer(value, source.Pointer)
		if err != nil || resolved == nil {
			return "", fmt.Errorf("body source field %q missing or null", source.Pointer)
		}
		body[field] = resolved
	}
	data, err := json.Marshal(body)
	return string(data), err
}

func readJSONSource(ctx context.Context, endpoint, path string) (any, error) {
	target, err := requestURL(endpoint, path)
	if err != nil || path == "" {
		return nil, errors.New("invalid JSON source path")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build JSON source request: %w", err)
	}
	if token := os.Getenv("SPEX_HTTP_BEARER_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Never follow redirects to another service with the operator token.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read JSON source: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JSON source returned HTTP %d or unreadable data", resp.StatusCode)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode JSON source: %w", err)
	}
	return value, nil
}

func requestURL(endpoint string, path string) (string, error) {
	base, err := url.Parse(endpoint + "/")
	if err != nil {
		return "", err
	}
	relative, err := url.Parse(strings.TrimLeft(path, "/"))
	if err != nil {
		return "", err
	}
	if relative.IsAbs() || relative.Host != "" {
		return "", errors.New("operation path must be relative")
	}
	return base.ResolveReference(relative).String(), nil
}

func requestJSON(
	ctx context.Context,
	op operation,
	target string,
) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, method(op), target, bytes.NewBufferString(op.Body))
	if err != nil {
		return 0, "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType(op))
	if token := os.Getenv("SPEX_HTTP_BEARER_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range op.Headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("read response body: %w", err)
	}
	body := string(bodyBytes)
	return resp.StatusCode, body, nil
}

func validateResponse(op operation, status int, body string) error {
	if status != op.ExpectedStatus {
		return fmt.Errorf(
			"unexpected HTTP status %d, expected %d, body: %s",
			status,
			op.ExpectedStatus,
			body,
		)
	}
	if strings.TrimSpace(op.ExpectedBody) == "" && len(op.ExpectedFields) == 0 && op.ExpectedArrayLength == nil {
		return nil
	}
	var actual any
	if err := json.Unmarshal([]byte(body), &actual); err != nil {
		return fmt.Errorf("decode actual JSON body: %w", err)
	}
	if strings.TrimSpace(op.ExpectedBody) != "" {
		var expected any
		if err := json.Unmarshal([]byte(op.ExpectedBody), &expected); err != nil {
			return fmt.Errorf("decode expected JSON body: %w", err)
		}
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("unexpected JSON body %s, expected %s", body, op.ExpectedBody)
		}
	}
	if op.ExpectedArrayLength != nil {
		array, ok := actual.([]any)
		if !ok || len(array) != *op.ExpectedArrayLength {
			return fmt.Errorf("expected JSON array length %d, body: %s", *op.ExpectedArrayLength, body)
		}
	}
	pointers := make([]string, 0, len(op.ExpectedFields))
	for pointer := range op.ExpectedFields {
		pointers = append(pointers, pointer)
	}
	sort.Strings(pointers)
	for _, pointer := range pointers {
		value, err := jsonPointer(actual, pointer)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(value, op.ExpectedFields[pointer]) {
			return fmt.Errorf("JSON field %q = %v, expected %v", pointer, value, op.ExpectedFields[pointer])
		}
	}
	return nil
}

// jsonPointer resolves RFC 6901 pointers, including array indexes.
func jsonPointer(value any, pointer string) (any, error) {
	if pointer == "" {
		return value, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q", pointer)
	}
	for _, token := range strings.Split(pointer[1:], "/") {
		for i := 0; i < len(token); i++ {
			if token[i] == '~' {
				if i+1 >= len(token) || (token[i+1] != '0' && token[i+1] != '1') {
					return nil, fmt.Errorf("invalid JSON pointer %q", pointer)
				}
				i++
			}
		}
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch node := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = node[token]
			if !ok {
				return nil, fmt.Errorf("missing JSON field %q", pointer)
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) || strconv.Itoa(index) != token {
				return nil, fmt.Errorf("invalid JSON array index in %q", pointer)
			}
			value = node[index]
		default:
			return nil, fmt.Errorf("cannot traverse JSON field %q", pointer)
		}
	}
	return value, nil
}

func method(op operation) string {
	if op.Method == "" {
		return http.MethodPost
	}
	return strings.ToUpper(op.Method)
}

func contentType(op operation) string {
	if op.ContentType == "" {
		return "application/json"
	}
	return op.ContentType
}

func writeResult(path string, result resultEnvelope) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write result file: %w", err)
	}
	return nil
}
