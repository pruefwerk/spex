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
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type mongoDBExpectRequest struct {
	URI          string
	Database     string
	Collection   string
	Username     string
	Password     string
	FilterFile   string
	MatchersFile string
	Timeout      time.Duration
	PollInterval time.Duration
}

type mongoDBPingRequest struct {
	URI      string
	Database string
	Username string
	Password string
	Timeout  time.Duration
}

func runMongoDBOperation(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("mongodb run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	operationFile := fs.String("operation-file", "", "lowered operation JSON file")
	resultFile := fs.String("result-file", "", "normalized result envelope path")
	timeoutValue := fs.String("timeout", "", "timeout override")
	pollIntervalValue := fs.String("poll-interval", "1s", "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "mongodb run"); err != nil {
		return err
	}
	if *operationFile == "" || *resultFile == "" {
		return fmt.Errorf("mongodb run requires --operation-file and --result-file")
	}
	operation, err := readLoweredOperation(*operationFile)
	if err != nil {
		return err
	}
	if operation.Provider != "mongodb" || (operation.OperationType != "mongodb.expect" && operation.OperationType != "mongodb.ping") {
		return fmt.Errorf("mongodb run cannot execute operation type %q from provider %q", operation.OperationType, operation.Provider)
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
	err = executeMongoDBLoweredOperation(operation, timeout, pollInterval)
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

func executeMongoDBLoweredOperation(operation probeLoweredOperation, timeout, pollInterval time.Duration) error {
	username, password := mongoDBCredentialsFromEnv()
	uri, _ := operation.Binding.With["uri"].(string)
	if uriFromEnv := os.Getenv("SPEX_MONGODB_URI"); uriFromEnv != "" && mongoDBURIUsesRuntimeEnv(uri) {
		uri = uriFromEnv
	}
	database, _ := operation.Binding.With["database"].(string)
	if operation.OperationType == "mongodb.ping" {
		return pingMongoDB(mongoDBPingRequest{
			URI:      uri,
			Database: database,
			Username: username,
			Password: password,
			Timeout:  timeout,
		})
	}
	dir, err := os.MkdirTemp("", "spex-mongodb-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	filter, _ := operation.With["filter"].(string)
	collection, _ := operation.With["collection"].(string)
	filterFile := filepath.Join(dir, "filter.json")
	matchersFile := filepath.Join(dir, "matchers.json")
	if err := os.WriteFile(filterFile, []byte(filter), 0o644); err != nil {
		return err
	}
	matchContent, err := json.Marshal(operation.With["match"])
	if err != nil {
		return err
	}
	if err := os.WriteFile(matchersFile, matchContent, 0o644); err != nil {
		return err
	}
	return expectMongoDB(mongoDBExpectRequest{
		URI:          uri,
		Database:     database,
		Collection:   collection,
		Username:     username,
		Password:     password,
		FilterFile:   filterFile,
		MatchersFile: matchersFile,
		Timeout:      timeout,
		PollInterval: pollInterval,
	})
}

func mongoDBURIUsesRuntimeEnv(uri string) bool {
	trimmed := strings.TrimSpace(uri)
	return trimmed == "" || strings.HasPrefix(trimmed, "{{ ssm ")
}

func pingMongoDB(req mongoDBPingRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), req.Timeout)
	defer cancel()
	client, err := connectMongoDB(ctx, req.URI, req.Username, req.Password)
	if err != nil {
		return err
	}
	defer client.Disconnect(context.Background())
	return client.Ping(ctx, nil)
}

func expectMongoDB(req mongoDBExpectRequest) error {
	filter, err := readMongoDBFilter(req.FilterFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), req.Timeout)
	defer cancel()

	client, err := connectMongoDB(ctx, req.URI, req.Username, req.Password)
	if err != nil {
		return err
	}
	defer client.Disconnect(context.Background())
	if err := client.Ping(ctx, nil); err != nil {
		return err
	}

	collection := client.Database(req.Database).Collection(req.Collection)
	var lastErr error
	for {
		var document bson.M
		err := collection.FindOne(ctx, filter).Decode(&document)
		if err == nil {
			if matchErr := evaluateMongoDBDocument(req.MatchersFile, document); matchErr == nil {
				return nil
			} else {
				lastErr = matchErr
			}
		} else if errors.Is(err, mongo.ErrNoDocuments) {
			lastErr = err
		} else {
			lastErr = err
		}

		timer := time.NewTimer(req.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				return fmt.Errorf("mongodb expectation timed out: %w", lastErr)
			}
			return fmt.Errorf("mongodb expectation timed out")
		case <-timer.C:
		}
	}
}

func connectMongoDB(ctx context.Context, uri, username, password string) (*mongo.Client, error) {
	clientOptions := options.Client().ApplyURI(uri)
	if username != "" || password != "" {
		clientOptions.SetAuth(options.Credential{
			Username: username,
			Password: password,
		})
	}
	return mongo.Connect(clientOptions)
}

func readMongoDBFilter(path string) (bson.M, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var filter bson.M
	if err := bson.UnmarshalExtJSON(content, true, &filter); err != nil {
		return nil, fmt.Errorf("mongodb filter: %w", err)
	}
	return filter, nil
}

func evaluateMongoDBDocument(matchersFile string, document bson.M) error {
	extended, err := bson.MarshalExtJSON(document, true, false)
	if err != nil {
		return err
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(extended))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("mongodb document: %w", err)
	}
	return EvaluateMatchersFileAgainstDocument(matchersFile, decoded)
}

func mongoDBCredentialsFromEnv() (string, string) {
	return os.Getenv("SPEX_MONGODB_USERNAME"), os.Getenv("SPEX_MONGODB_PASSWORD")
}
