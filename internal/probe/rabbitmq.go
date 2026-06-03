package probe

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

type rabbitMQPublishRequest struct {
	URI        string
	Exchange   string
	RoutingKey string
	Username   string
	Password   string
	Payload    []byte
	Timeout    time.Duration
}

type rabbitMQExpectRequest struct {
	URI          string
	Queue        string
	Username     string
	Password     string
	MatchersFile string
	Timeout      time.Duration
	PollInterval time.Duration
}

func runRabbitMQOperation(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("rabbitmq run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	operationFile := fs.String("operation-file", "", "lowered operation JSON file")
	resultFile := fs.String("result-file", "", "normalized result envelope path")
	timeoutValue := fs.String("timeout", "", "timeout override")
	pollIntervalValue := fs.String("poll-interval", "1s", "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "rabbitmq run"); err != nil {
		return err
	}
	if *operationFile == "" || *resultFile == "" {
		return fmt.Errorf("rabbitmq run requires --operation-file and --result-file")
	}
	operation, err := readLoweredOperation(*operationFile)
	if err != nil {
		return err
	}
	if operation.Provider != "rabbitmq" || (operation.OperationType != "rabbitmq.publish" && operation.OperationType != "rabbitmq.expect") {
		return fmt.Errorf("rabbitmq run cannot execute operation type %q from provider %q", operation.OperationType, operation.Provider)
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
	err = executeRabbitMQLoweredOperation(operation, timeout, pollInterval)
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

func executeRabbitMQLoweredOperation(operation probeLoweredOperation, timeout, pollInterval time.Duration) error {
	uri, _ := operation.Binding.With["uri"].(string)
	username, password := rabbitMQCredentialsFromEnv()
	switch operation.OperationType {
	case "rabbitmq.publish":
		exchange, _ := operation.With["exchange"].(string)
		routingKey, _ := operation.With["routingKey"].(string)
		payload, _ := operation.With["payload"].(string)
		return publishRabbitMQ(rabbitMQPublishRequest{
			URI:        uri,
			Exchange:   exchange,
			RoutingKey: routingKey,
			Username:   username,
			Password:   password,
			Payload:    []byte(payload),
			Timeout:    timeout,
		})
	case "rabbitmq.expect":
		dir, err := os.MkdirTemp("", "spex-rabbitmq-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		matchersFile := filepath.Join(dir, "matchers.json")
		matchContent, err := json.Marshal(operation.With["match"])
		if err != nil {
			return err
		}
		if err := os.WriteFile(matchersFile, matchContent, 0o644); err != nil {
			return err
		}
		queue, _ := operation.With["queue"].(string)
		return expectRabbitMQ(rabbitMQExpectRequest{
			URI:          uri,
			Queue:        queue,
			Username:     username,
			Password:     password,
			MatchersFile: matchersFile,
			Timeout:      timeout,
			PollInterval: pollInterval,
		})
	default:
		return fmt.Errorf("unsupported RabbitMQ operation type %q", operation.OperationType)
	}
}

func publishRabbitMQ(req rabbitMQPublishRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), req.Timeout)
	defer cancel()
	conn, err := amqp.Dial(rabbitMQURI(req.URI, req.Username, req.Password))
	if err != nil {
		return err
	}
	defer conn.Close()
	channel, err := conn.Channel()
	if err != nil {
		return err
	}
	defer channel.Close()
	return channel.PublishWithContext(ctx, req.Exchange, req.RoutingKey, false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        req.Payload,
	})
}

func expectRabbitMQ(req rabbitMQExpectRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), req.Timeout)
	defer cancel()
	conn, err := amqp.Dial(rabbitMQURI(req.URI, req.Username, req.Password))
	if err != nil {
		return err
	}
	defer conn.Close()
	channel, err := conn.Channel()
	if err != nil {
		return err
	}
	defer channel.Close()

	var lastErr error
	for {
		delivery, ok, err := channel.Get(req.Queue, false)
		if err != nil {
			lastErr = err
		} else if ok {
			if matchErr := EvaluateMatchersBytes(req.MatchersFile, delivery.Body); matchErr == nil {
				if err := delivery.Ack(false); err != nil {
					return err
				}
				return nil
			} else {
				lastErr = matchErr
				if err := delivery.Ack(false); err != nil {
					return err
				}
			}
		} else {
			lastErr = fmt.Errorf("queue %q had no messages", req.Queue)
		}

		timer := time.NewTimer(req.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				return fmt.Errorf("rabbitmq expectation timed out: %w", lastErr)
			}
			return fmt.Errorf("rabbitmq expectation timed out")
		case <-timer.C:
		}
	}
}

func rabbitMQURI(raw, username, password string) string {
	if username == "" && password == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.User = url.UserPassword(username, password)
	return parsed.String()
}

func rabbitMQCredentialsFromEnv() (string, string) {
	return os.Getenv("SPEX_RABBITMQ_USERNAME"), os.Getenv("SPEX_RABBITMQ_PASSWORD")
}
