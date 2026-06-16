package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var hawkbitHTTPClient = http.DefaultClient

func runHawkbitOperation(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("hawkbit run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	operationFile := fs.String("operation-file", "", "lowered operation JSON file")
	resultFile := fs.String("result-file", "", "normalized result envelope path")
	timeoutValue := fs.String("timeout", "", "timeout override")
	pollIntervalValue := fs.String("poll-interval", "1s", "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "hawkbit run"); err != nil {
		return err
	}
	if *operationFile == "" || *resultFile == "" {
		return fmt.Errorf("hawkbit run requires --operation-file and --result-file")
	}
	operation, err := readLoweredOperation(*operationFile)
	if err != nil {
		return err
	}
	if operation.Provider != "hawkbit" || !hawkbitCanExecute(operation.OperationType) {
		return fmt.Errorf("hawkbit run cannot execute operation type %q from provider %q", operation.OperationType, operation.Provider)
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
	err = executeHawkbitLoweredOperation(operation, timeout, pollInterval)
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

func hawkbitCanExecute(operationType string) bool {
	switch operationType {
	case "hawkbit.managementGet", "hawkbit.managementPost", "hawkbit.directDeviceGet", "hawkbit.publishGatewayMessage", "hawkbit.expectGatewayMessage":
		return true
	default:
		return false
	}
}

func executeHawkbitLoweredOperation(operation probeLoweredOperation, timeout, pollInterval time.Duration) error {
	switch operation.OperationType {
	case "hawkbit.managementGet":
		return executeHawkbitHTTPRequest(operation, http.MethodGet, timeout)
	case "hawkbit.managementPost":
		return executeHawkbitHTTPRequest(operation, http.MethodPost, timeout)
	case "hawkbit.directDeviceGet":
		return executeHawkbitHTTPRequest(operation, http.MethodGet, timeout)
	case "hawkbit.publishGatewayMessage":
		brokerURL, _ := operation.Binding.With["mqttBrokerURL"].(string)
		if brokerURLFromEnv := os.Getenv("SPEX_MQTT_BROKER_URL"); brokerURLFromEnv != "" && mqttBrokerURLUsesRuntimeEnv(brokerURL) {
			brokerURL = brokerURLFromEnv
		}
		clientID, _ := operation.With["clientId"].(string)
		if clientID == "" {
			clientID = "spex-hawkbit-probe"
		}
		payload, _ := operation.With["payload"].(string)
		username, password := mqttCredentialsFromEnv()
		return publishMQTT(mqttPublishRequest{
			BrokerURL: brokerURL,
			Topic:     hawkbitMQTTTopic(operation),
			ClientID:  clientID,
			Username:  username,
			Password:  password,
			Payload:   []byte(payload),
			Timeout:   timeout,
			QoS:       1,
		})
	case "hawkbit.expectGatewayMessage":
		dir, err := os.MkdirTemp("", "spex-hawkbit-*")
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
		uri, _ := operation.Binding.With["rabbitmqURI"].(string)
		queue, _ := operation.With["queue"].(string)
		username, password := rabbitMQCredentialsFromEnv()
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
		return fmt.Errorf("unsupported Hawkbit operation type %q", operation.OperationType)
	}
}

func executeHawkbitHTTPRequest(operation probeLoweredOperation, method string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	url := hawkbitRequestURL(operation)
	payload, _ := operation.With["payload"].(string)
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader([]byte(payload)))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/hal+json, application/json")
	if method == http.MethodPost || payload != "" {
		request.Header.Set("Content-Type", hawkbitContentType(operation))
	}
	applyHawkbitAuth(operation, request)
	response, err := hawkbitHTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	expected := hawkbitExpectedStatus(operation, method)
	if response.StatusCode != expected {
		return fmt.Errorf("hawkbit HTTP %s %s returned HTTP %d, expected %d: %s", method, url, response.StatusCode, expected, string(body))
	}
	if matchers, ok := operation.With["match"]; ok && matchers != nil {
		if err := evaluateMatchersAgainstValue(matchers, decodeHawkbitResponseBody(body)); err != nil {
			return err
		}
	}
	return nil
}

func hawkbitRequestURL(operation probeLoweredOperation) string {
	baseURL, _ := operation.Binding.With["baseURL"].(string)
	if baseURLFromEnv := os.Getenv("SPEX_HAWKBIT_BASE_URL"); baseURLFromEnv != "" && mqttBrokerURLUsesRuntimeEnv(baseURL) {
		baseURL = baseURLFromEnv
	}
	baseURL = strings.TrimRight(baseURL, "/")
	switch operation.OperationType {
	case "hawkbit.directDeviceGet":
		tenant, _ := operation.With["tenant"].(string)
		if tenant == "" {
			tenant, _ = operation.Binding.With["tenant"].(string)
		}
		if tenant == "" {
			tenant = "DEFAULT"
		}
		ddiAPIPath, _ := operation.Binding.With["ddiApiPath"].(string)
		if ddiAPIPath == "" {
			ddiAPIPath = "/controller/v1"
		}
		controllerID, _ := operation.With["controllerId"].(string)
		return baseURL + "/" + strings.Trim(tenant, "/") + "/" + strings.Trim(ddiAPIPath, "/") + "/" + strings.Trim(controllerID, "/")
	default:
		managementAPIPath, _ := operation.Binding.With["managementApiPath"].(string)
		if managementAPIPath == "" {
			managementAPIPath = "/rest/v1"
		}
		resource, _ := operation.With["resource"].(string)
		return baseURL + "/" + strings.Trim(managementAPIPath, "/") + "/" + strings.TrimLeft(resource, "/")
	}
}

func hawkbitContentType(operation probeLoweredOperation) string {
	contentType, _ := operation.With["contentType"].(string)
	if contentType == "" {
		contentType, _ = operation.Binding.With["contentType"].(string)
	}
	if contentType == "" {
		contentType = "application/hal+json"
	}
	return contentType
}

func applyHawkbitAuth(operation probeLoweredOperation, request *http.Request) {
	if operation.OperationType == "hawkbit.directDeviceGet" {
		tokenType, _ := operation.With["tokenType"].(string)
		switch tokenType {
		case "gateway":
			if token := os.Getenv("SPEX_HAWKBIT_GATEWAY_TOKEN"); token != "" {
				request.Header.Set("Authorization", "GatewayToken "+token)
			}
		default:
			if token := os.Getenv("SPEX_HAWKBIT_TARGET_TOKEN"); token != "" {
				request.Header.Set("Authorization", "TargetToken "+token)
			}
		}
		return
	}
	username := os.Getenv("SPEX_HAWKBIT_USERNAME")
	password := os.Getenv("SPEX_HAWKBIT_PASSWORD")
	if username != "" || password != "" {
		request.SetBasicAuth(username, password)
	}
}

func hawkbitExpectedStatus(operation probeLoweredOperation, method string) int {
	value := operation.With["expectedStatus"]
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case string:
		if parsed, err := strconv.Atoi(typed); err == nil {
			return parsed
		}
	}
	if method == http.MethodPost {
		return http.StatusCreated
	}
	return http.StatusOK
}

func decodeHawkbitResponseBody(body []byte) any {
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return map[string]any{"body": string(body)}
	}
	return decoded
}

func hawkbitMQTTTopic(operation probeLoweredOperation) string {
	gatewayID, _ := operation.With["gatewayId"].(string)
	messageType, _ := operation.With["messageType"].(string)
	topicStyle, _ := operation.With["topicStyle"].(string)
	if topicStyle == "" {
		topicStyle, _ = operation.With["protocolVersion"].(string)
	}
	direction, _ := operation.With["direction"].(string)
	if direction == "" {
		direction = "gw2dm"
	}
	switch normalizeHawkbitProbeProtocolVersion(topicStyle) {
	case "old":
		return fmt.Sprintf("/gw-%s/dm/hawkbit/%s/%s", gatewayID, direction, messageType)
	default:
		return fmt.Sprintf("gateway/%s/hawkbit/%s/%s", gatewayID, direction, messageType)
	}
}

func normalizeHawkbitProbeProtocolVersion(value string) string {
	switch strings.ToLower(value) {
	case "legacy", "old", "v1":
		return "old"
	default:
		return "new"
	}
}
