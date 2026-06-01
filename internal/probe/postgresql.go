package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
