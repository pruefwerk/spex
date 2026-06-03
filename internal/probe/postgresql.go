package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
)

type postgreSQLExpectRequest struct {
	URI          string
	Username     string
	Password     string
	QueryFile    string
	ArgsFile     string
	MatchersFile string
	Timeout      time.Duration
	PollInterval time.Duration
}

type probeLoweredOperation struct {
	OperationID   string              `json:"operationId"`
	OperationType string              `json:"operationType"`
	Provider      string              `json:"provider"`
	Binding       probeLoweredBinding `json:"binding"`
	With          map[string]any      `json:"with"`
	Timeout       string              `json:"timeout"`
	DependsOn     []string            `json:"dependsOn"`
}

type probeLoweredBinding struct {
	Name string         `json:"name"`
	Kind string         `json:"kind"`
	With map[string]any `json:"with"`
}

type probeResultEnvelope struct {
	OperationID   string                  `json:"operationId"`
	OperationType string                  `json:"operationType"`
	Provider      string                  `json:"provider"`
	Status        string                  `json:"status"`
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

func runPostgreSQLOperation(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("postgresql run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	operationFile := fs.String("operation-file", "", "lowered operation JSON file")
	resultFile := fs.String("result-file", "", "normalized result envelope path")
	timeoutValue := fs.String("timeout", "", "timeout override")
	pollIntervalValue := fs.String("poll-interval", "1s", "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "postgresql run"); err != nil {
		return err
	}
	if *operationFile == "" || *resultFile == "" {
		return fmt.Errorf("postgresql run requires --operation-file and --result-file")
	}
	operation, err := readLoweredOperation(*operationFile)
	if err != nil {
		return err
	}
	if operation.OperationType != "postgresql.expect" || operation.Provider != "postgresql" {
		return fmt.Errorf("postgresql run cannot execute operation type %q from provider %q", operation.OperationType, operation.Provider)
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
	err = executePostgreSQLLoweredOperation(operation, timeout, pollInterval)
	envelope := probeResultEnvelope{
		OperationID:   operation.OperationID,
		OperationType: operation.OperationType,
		Provider:      operation.Provider,
		Status:        "passed",
		Result:        map[string]any{},
		Evidence:      []probeEvidenceEnvelope{},
		Diagnostics:   []probeDiagnostic{},
	}
	if err != nil {
		envelope.Status = "failed"
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

func executePostgreSQLLoweredOperation(operation probeLoweredOperation, timeout, pollInterval time.Duration) error {
	dir, err := os.MkdirTemp("", "spex-postgresql-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	query, _ := operation.With["query"].(string)
	args := loweredStringSlice(operation.With["args"])
	match := operation.With["match"]
	queryFile := filepath.Join(dir, "query.sql")
	argsFile := filepath.Join(dir, "args.json")
	matchersFile := filepath.Join(dir, "matchers.json")
	if err := os.WriteFile(queryFile, []byte(query), 0o644); err != nil {
		return err
	}
	argsContent, err := json.Marshal(args)
	if err != nil {
		return err
	}
	if err := os.WriteFile(argsFile, argsContent, 0o644); err != nil {
		return err
	}
	matchContent, err := json.Marshal(match)
	if err != nil {
		return err
	}
	if err := os.WriteFile(matchersFile, matchContent, 0o644); err != nil {
		return err
	}
	username, password := postgreSQLCredentialsFromEnv()
	uri, _ := operation.Binding.With["uri"].(string)
	return expectPostgreSQL(postgreSQLExpectRequest{
		URI:          uri,
		Username:     username,
		Password:     password,
		QueryFile:    queryFile,
		ArgsFile:     argsFile,
		MatchersFile: matchersFile,
		Timeout:      timeout,
		PollInterval: pollInterval,
	})
}

func readLoweredOperation(path string) (probeLoweredOperation, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return probeLoweredOperation{}, err
	}
	var operation probeLoweredOperation
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&operation); err != nil {
		return probeLoweredOperation{}, fmt.Errorf("lowered operation: %w", err)
	}
	return operation, nil
}

func writeProbeResultEnvelope(path string, envelope probeResultEnvelope) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(content, '\n'), 0o644)
}

func writeProbeResultEnvelopeToWriter(w io.Writer, envelope probeResultEnvelope) error {
	content, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(content))
	return err
}

func loweredStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			switch v := item.(type) {
			case string:
				out = append(out, v)
			default:
				out = append(out, fmt.Sprint(v))
			}
		}
		return out
	default:
		return nil
	}
}

func expectPostgreSQL(req postgreSQLExpectRequest) error {
	query, err := os.ReadFile(req.QueryFile)
	if err != nil {
		return err
	}
	args, err := readPostgreSQLArgs(req.ArgsFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), req.Timeout)
	defer cancel()

	config, err := pgx.ParseConfig(req.URI)
	if err != nil {
		return err
	}
	if req.Username != "" || req.Password != "" {
		config.User = req.Username
		config.Password = req.Password
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())

	var lastErr error
	for {
		row, err := queryPostgreSQLRow(ctx, conn, string(query), args)
		if err == nil {
			if matchErr := EvaluateMatchersFileAgainstDocument(req.MatchersFile, row); matchErr == nil {
				return nil
			} else {
				lastErr = matchErr
			}
		} else {
			lastErr = err
		}

		timer := time.NewTimer(req.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				return fmt.Errorf("postgresql expectation timed out: %w", lastErr)
			}
			return fmt.Errorf("postgresql expectation timed out")
		case <-timer.C:
		}
	}
}

func readPostgreSQLArgs(path string) ([]any, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw []string
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("postgresql args: %w", err)
	}
	args := make([]any, 0, len(raw))
	for _, value := range raw {
		args = append(args, value)
	}
	return args, nil
}

func queryPostgreSQLRow(ctx context.Context, conn *pgx.Conn, query string, args []any) (map[string]any, error) {
	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, pgx.ErrNoRows
	}
	values, err := rows.Values()
	if err != nil {
		return nil, err
	}
	fields := rows.FieldDescriptions()
	if len(fields) != len(values) {
		return nil, fmt.Errorf("postgresql row field count mismatch")
	}
	out := map[string]any{}
	for i, field := range fields {
		out[field.Name] = normalizePostgreSQLValue(values[i])
	}
	if rows.Next() {
		return nil, errors.New("postgresql query returned more than one row")
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func normalizePostgreSQLValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	default:
		return typed
	}
}

func postgreSQLCredentialsFromEnv() (string, string) {
	return os.Getenv("SPEX_POSTGRESQL_USERNAME"), os.Getenv("SPEX_POSTGRESQL_PASSWORD")
}
