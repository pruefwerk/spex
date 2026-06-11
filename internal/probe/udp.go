package probe

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

func runUDPOperation(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("udp run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	operationFile := fs.String("operation-file", "", "lowered operation JSON file")
	resultFile := fs.String("result-file", "", "normalized result envelope path")
	timeoutValue := fs.String("timeout", "", "timeout override")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "udp run"); err != nil {
		return err
	}
	if *operationFile == "" || *resultFile == "" {
		return fmt.Errorf("udp run requires --operation-file and --result-file")
	}
	operation, err := readLoweredOperation(*operationFile)
	if err != nil {
		return err
	}
	if operation.Provider != "udp" || operation.OperationType != "udp.send" {
		return fmt.Errorf("udp run cannot execute operation type %q from provider %q", operation.OperationType, operation.Provider)
	}
	timeoutText := operation.Timeout
	if *timeoutValue != "" {
		timeoutText = *timeoutValue
	}
	timeout, err := time.ParseDuration(timeoutText)
	if err != nil {
		return fmt.Errorf("invalid timeout: %w", err)
	}
	if timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	err = executeUDPLoweredOperation(operation, timeout)
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

func executeUDPLoweredOperation(operation probeLoweredOperation, timeout time.Duration) error {
	host := stringFromLoweredMaps("host", operation.With, operation.Binding.With)
	port, err := intFromLoweredMaps("port", operation.With, operation.Binding.With)
	if err != nil {
		return err
	}
	payload, _ := operation.With["payload"].(string)
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("udp.send requires host")
	}
	if port <= 0 || port > 65535 {
		return fmt.Errorf("udp.send requires port between 1 and 65535")
	}
	if payload == "" {
		return fmt.Errorf("udp.send requires payload")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	address := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", address)
	if err != nil {
		return fmt.Errorf("udp dial %s: %w", address, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write([]byte(payload)); err != nil {
		return fmt.Errorf("udp send %s: %w", address, err)
	}
	return nil
}

func stringFromLoweredMaps(key string, maps ...map[string]any) string {
	for _, values := range maps {
		if value, ok := values[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func intFromLoweredMaps(key string, maps ...map[string]any) (int, error) {
	for _, values := range maps {
		switch value := values[key].(type) {
		case int:
			return value, nil
		case float64:
			return int(value), nil
		case string:
			if strings.TrimSpace(value) == "" {
				continue
			}
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return 0, fmt.Errorf("udp.send %s must be an integer: %w", key, err)
			}
			return parsed, nil
		}
	}
	return 0, fmt.Errorf("udp.send requires %s", key)
}
