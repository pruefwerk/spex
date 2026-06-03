package probe

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type influxDBQueryResult struct {
	Rows []map[string]any `json:"rows"`
}

func runInfluxDBOperation(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("influxdb run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	operationFile := fs.String("operation-file", "", "lowered operation JSON file")
	resultFile := fs.String("result-file", "", "normalized result envelope path")
	timeoutValue := fs.String("timeout", "", "timeout override")
	pollIntervalValue := fs.String("poll-interval", "1s", "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "influxdb run"); err != nil {
		return err
	}
	if *operationFile == "" || *resultFile == "" {
		return fmt.Errorf("influxdb run requires --operation-file and --result-file")
	}
	operation, err := readLoweredOperation(*operationFile)
	if err != nil {
		return err
	}
	if operation.OperationType != "influxdb.expect" || operation.Provider != "influxdb" {
		return fmt.Errorf("influxdb run cannot execute operation type %q from provider %q", operation.OperationType, operation.Provider)
	}
	timeoutText := operation.Timeout
	if *timeoutValue != "" {
		timeoutText = *timeoutValue
	}
	timeout, err := time.ParseDuration(timeoutText)
	if err != nil {
		return fmt.Errorf("invalid timeout: %w", err)
	}
	pollInterval, err := time.ParseDuration(*pollIntervalValue)
	if err != nil {
		return fmt.Errorf("invalid --poll-interval: %w", err)
	}
	if timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	if pollInterval <= 0 {
		return fmt.Errorf("--poll-interval must be positive")
	}
	result, err := executeInfluxDBLoweredOperation(operation, timeout, pollInterval)
	envelopeResult := map[string]any{}
	if result != nil {
		envelopeResult["rowCount"] = len(result.Rows)
	}
	envelope := probeResultEnvelope{
		OperationID:   operation.OperationID,
		OperationType: operation.OperationType,
		Provider:      operation.Provider,
		Status:        "passed",
		Result:        envelopeResult,
		Evidence:      []probeEvidenceEnvelope{},
		Diagnostics:   []probeDiagnostic{},
	}
	if err != nil {
		envelope.Status = "failed"
		envelope.Result = map[string]any{}
		envelope.Diagnostics = append(envelope.Diagnostics, probeDiagnostic{Severity: "error", Message: err.Error()})
	}
	if writeErr := writeProbeResultEnvelope(*resultFile, envelope); writeErr != nil {
		return writeErr
	}
	if writeErr := writeProbeResultEnvelopeToWriter(stdout, envelope); writeErr != nil {
		return writeErr
	}
	return err
}

func executeInfluxDBLoweredOperation(operation probeLoweredOperation, timeout, pollInterval time.Duration) (*influxDBQueryResult, error) {
	dir, err := os.MkdirTemp("", "spex-influxdb-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	matchersFile := filepath.Join(dir, "matchers.json")
	matchContent, err := json.Marshal(operation.With["match"])
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(matchersFile, matchContent, 0o644); err != nil {
		return nil, err
	}
	var lastResult *influxDBQueryResult
	err = influxDBEventually(timeout, pollInterval, func() error {
		result, err := queryInfluxDB(operation)
		if err != nil {
			return err
		}
		lastResult = result
		document := map[string]any{"rows": influxDBRowsAsAny(result.Rows)}
		return EvaluateMatchersFileAgainstDocument(matchersFile, document)
	})
	if err != nil {
		return lastResult, err
	}
	return lastResult, nil
}

func queryInfluxDB(operation probeLoweredOperation) (*influxDBQueryResult, error) {
	version, _ := operation.Binding.With["version"].(string)
	endpoint, _ := operation.Binding.With["endpoint"].(string)
	query, _ := operation.With["query"].(string)
	switch version {
	case "v2":
		org, _ := operation.Binding.With["org"].(string)
		return queryInfluxDBV2(endpoint, org, query)
	case "v3":
		database, _ := operation.Binding.With["database"].(string)
		language, _ := operation.With["language"].(string)
		if language == "" {
			language = "sql"
		}
		return queryInfluxDBV3(endpoint, database, language, query)
	default:
		return nil, fmt.Errorf("influxdb binding version must be v2 or v3")
	}
}

func queryInfluxDBV2(endpoint, org, query string) (*influxDBQueryResult, error) {
	if endpoint == "" || org == "" {
		return nil, fmt.Errorf("influxdb v2 requires endpoint and org")
	}
	target, err := url.Parse(strings.TrimRight(endpoint, "/") + "/api/v2/query")
	if err != nil {
		return nil, err
	}
	values := target.Query()
	values.Set("org", org)
	target.RawQuery = values.Encode()
	body, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/csv")
	setInfluxDBToken(request)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("influxdb v2 query returned HTTP %d: %s", response.StatusCode, string(content))
	}
	rows, err := parseInfluxDBCSVRows(content)
	if err != nil {
		return nil, err
	}
	return &influxDBQueryResult{Rows: rows}, nil
}

func queryInfluxDBV3(endpoint, database, language, query string) (*influxDBQueryResult, error) {
	if endpoint == "" || database == "" {
		return nil, fmt.Errorf("influxdb v3 requires endpoint and database")
	}
	path := "/api/v3/query_sql"
	switch language {
	case "", "sql":
	case "influxql":
		path = "/api/v3/query_influxql"
	default:
		return nil, fmt.Errorf("influxdb v3 language must be sql or influxql")
	}
	body, err := json.Marshal(map[string]any{"db": database, "database": database, "query": query})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, strings.TrimRight(endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	setInfluxDBToken(request)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("influxdb v3 query returned HTTP %d: %s", response.StatusCode, string(content))
	}
	rows, err := parseInfluxDBJSONRows(content)
	if err != nil {
		return nil, err
	}
	return &influxDBQueryResult{Rows: rows}, nil
}

func setInfluxDBToken(request *http.Request) {
	if token := os.Getenv("SPEX_INFLUXDB_TOKEN"); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
}

func parseInfluxDBCSVRows(content []byte) ([]map[string]any, error) {
	reader := csv.NewReader(bytes.NewReader(content))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("influxdb csv: %w", err)
	}
	var header []string
	var rows []map[string]any
	for _, record := range records {
		if len(record) == 0 || strings.HasPrefix(record[0], "#") {
			continue
		}
		if header == nil {
			header = record
			continue
		}
		row := map[string]any{}
		for i, key := range header {
			if key == "" || i >= len(record) {
				continue
			}
			row[key] = parseInfluxDBScalar(record[i])
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func parseInfluxDBJSONRows(content []byte) ([]map[string]any, error) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err == nil {
		return rowsFromInfluxDBJSON(decoded)
	}
	var rows []map[string]any
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var row map[string]any
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.UseNumber()
		if err := decoder.Decode(&row); err != nil {
			return nil, fmt.Errorf("influxdb jsonl: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func rowsFromInfluxDBJSON(decoded any) ([]map[string]any, error) {
	switch value := decoded.(type) {
	case []any:
		return rowsFromJSONArray(value)
	case map[string]any:
		if rows, ok := value["rows"].([]any); ok {
			return rowsFromJSONArray(rows)
		}
		if data, ok := value["data"].([]any); ok {
			return rowsFromJSONArray(data)
		}
		return []map[string]any{value}, nil
	default:
		return nil, fmt.Errorf("influxdb json result must be object or array")
	}
}

func rowsFromJSONArray(values []any) ([]map[string]any, error) {
	rows := make([]map[string]any, 0, len(values))
	for i, item := range values {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("influxdb json row[%d] must be object", i)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func parseInfluxDBScalar(value string) any {
	if value == "" {
		return ""
	}
	if value == "true" {
		return true
	}
	if value == "false" {
		return false
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return json.Number(value)
	}
	return value
}

func influxDBRowsAsAny(rows []map[string]any) []any {
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	return out
}

func influxDBEventually(timeout, pollInterval time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().Add(pollInterval).After(deadline) {
			return lastErr
		}
		time.Sleep(pollInterval)
	}
}
