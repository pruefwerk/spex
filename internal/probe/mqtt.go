package probe

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type mqttPublishRequest struct {
	BrokerURL string
	Topic     string
	ClientID  string
	Username  string
	Password  string
	Payload   []byte
	Timeout   time.Duration
	QoS       byte
}

type mqttRoundTripRequest struct {
	BrokerURL    string
	Topic        string
	ClientID     string
	ClientMode   string
	Username     string
	Password     string
	Payload      []byte
	MatchersFile string
	Timeout      time.Duration
	QoS          byte
}

type mqttPublisher interface {
	Publish(mqttPublishRequest) error
	RoundTrip(mqttRoundTripRequest) error
}

type pahoMQTTPublisher struct{}

var mqttClient mqttPublisher = pahoMQTTPublisher{}

func runMQTTOperation(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("mqtt run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	operationFile := fs.String("operation-file", "", "lowered operation JSON file")
	resultFile := fs.String("result-file", "", "normalized result envelope path")
	timeoutValue := fs.String("timeout", "", "timeout override")
	pollIntervalValue := fs.String("poll-interval", "1s", "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "mqtt run"); err != nil {
		return err
	}
	if *operationFile == "" || *resultFile == "" {
		return fmt.Errorf("mqtt run requires --operation-file and --result-file")
	}
	operation, err := readLoweredOperation(*operationFile)
	if err != nil {
		return err
	}
	if operation.Provider != "mqtt" || (operation.OperationType != "mqtt.publish" && operation.OperationType != "mqtt.roundtrip") {
		return fmt.Errorf("mqtt run cannot execute operation type %q from provider %q", operation.OperationType, operation.Provider)
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
	err = executeMQTTLoweredOperation(operation, timeout)
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

func executeMQTTLoweredOperation(operation probeLoweredOperation, timeout time.Duration) error {
	brokerURL, _ := operation.Binding.With["brokerURL"].(string)
	if brokerURLFromEnv := os.Getenv("SPEX_MQTT_BROKER_URL"); brokerURLFromEnv != "" && (brokerURL == "" || strings.HasPrefix(brokerURL, "aws-ssm:")) {
		brokerURL = brokerURLFromEnv
	}
	topic, _ := operation.With["topic"].(string)
	clientID, _ := operation.With["clientId"].(string)
	if clientID == "" {
		clientID = "spex-probe"
	}
	payload, _ := operation.With["payload"].(string)
	username, password := mqttCredentialsFromEnv()
	switch operation.OperationType {
	case "mqtt.publish":
		return publishMQTT(mqttPublishRequest{
			BrokerURL: brokerURL,
			Topic:     topic,
			ClientID:  clientID,
			Username:  username,
			Password:  password,
			Payload:   []byte(payload),
			Timeout:   timeout,
			QoS:       1,
		})
	case "mqtt.roundtrip":
		dir, err := os.MkdirTemp("", "spex-mqtt-*")
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
		clientMode, _ := operation.With["clientMode"].(string)
		return roundTripMQTT(mqttRoundTripRequest{
			BrokerURL:    brokerURL,
			Topic:        topic,
			ClientID:     clientID,
			ClientMode:   clientMode,
			Username:     username,
			Password:     password,
			Payload:      []byte(payload),
			MatchersFile: matchersFile,
			Timeout:      timeout,
			QoS:          1,
		})
	default:
		return fmt.Errorf("unsupported MQTT operation type %q", operation.OperationType)
	}
}

func publishMQTT(req mqttPublishRequest) error {
	return mqttClient.Publish(req)
}

func roundTripMQTT(req mqttRoundTripRequest) error {
	return mqttClient.RoundTrip(req)
}

func (pahoMQTTPublisher) Publish(req mqttPublishRequest) error {
	options := mqttClientOptions(req.BrokerURL, req.ClientID, req.Username, req.Password, req.Timeout)

	client := mqtt.NewClient(options)
	connect := client.Connect()
	if !connect.WaitTimeout(req.Timeout) {
		return fmt.Errorf("mqtt connect timed out")
	}
	if err := connect.Error(); err != nil {
		return fmt.Errorf("mqtt connect: %w", err)
	}
	defer client.Disconnect(250)

	publish := client.Publish(req.Topic, req.QoS, false, req.Payload)
	if !publish.WaitTimeout(req.Timeout) {
		return fmt.Errorf("mqtt publish timed out")
	}
	if err := publish.Error(); err != nil {
		return fmt.Errorf("mqtt publish: %w", err)
	}
	return nil
}

func (pahoMQTTPublisher) RoundTrip(req mqttRoundTripRequest) error {
	if req.ClientMode == "" {
		req.ClientMode = "separate"
	}
	if req.ClientMode == "shared" {
		return roundTripMQTTShared(req)
	}
	return roundTripMQTTSeparate(req)
}

func roundTripMQTTSeparate(req mqttRoundTripRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), req.Timeout)
	defer cancel()

	subscriber := mqtt.NewClient(mqttClientOptions(req.BrokerURL, req.ClientID+"-sub", req.Username, req.Password, req.Timeout))
	connect := subscriber.Connect()
	if !connect.WaitTimeout(mqttRemainingTimeout(ctx)) {
		return fmt.Errorf("mqtt subscriber connect timed out")
	}
	if err := connect.Error(); err != nil {
		return fmt.Errorf("mqtt subscriber connect: %w", err)
	}
	defer subscriber.Disconnect(250)

	messages := make(chan []byte, 16)
	subscribe := subscriber.Subscribe(req.Topic, req.QoS, func(_ mqtt.Client, message mqtt.Message) {
		payload := append([]byte(nil), message.Payload()...)
		select {
		case messages <- payload:
		case <-ctx.Done():
		}
	})
	if !subscribe.WaitTimeout(mqttRemainingTimeout(ctx)) {
		return fmt.Errorf("mqtt subscribe timed out")
	}
	if err := subscribe.Error(); err != nil {
		return fmt.Errorf("mqtt subscribe: %w", err)
	}
	subscribeResult := mqttSubscribeResult(req.Topic, subscribe)

	publisher := mqtt.NewClient(mqttClientOptions(req.BrokerURL, req.ClientID+"-pub", req.Username, req.Password, req.Timeout))
	connect = publisher.Connect()
	if !connect.WaitTimeout(mqttRemainingTimeout(ctx)) {
		return fmt.Errorf("mqtt publisher connect timed out")
	}
	if err := connect.Error(); err != nil {
		return fmt.Errorf("mqtt publisher connect: %w", err)
	}
	defer publisher.Disconnect(250)

	publish := publisher.Publish(req.Topic, req.QoS, false, req.Payload)
	if !publish.WaitTimeout(mqttRemainingTimeout(ctx)) {
		return fmt.Errorf("mqtt publish timed out")
	}
	if err := publish.Error(); err != nil {
		return fmt.Errorf("mqtt publish: %w", err)
	}

	var lastErr error
	received := 0
	for {
		select {
		case payload := <-messages:
			received++
			if err := EvaluateMatchersBytes(req.MatchersFile, payload); err == nil {
				return nil
			} else {
				lastErr = err
			}
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("mqtt roundtrip expectation timed out on topic %q using separate clients after receiving %d message(s); subscription %s: %w", req.Topic, received, subscribeResult, lastErr)
			}
			return fmt.Errorf("mqtt roundtrip expectation timed out on topic %q using separate clients without receiving messages; subscription %s", req.Topic, subscribeResult)
		}
	}
}

func roundTripMQTTShared(req mqttRoundTripRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), req.Timeout)
	defer cancel()

	client := mqtt.NewClient(mqttClientOptions(req.BrokerURL, req.ClientID+"-shared", req.Username, req.Password, req.Timeout))
	connect := client.Connect()
	if !connect.WaitTimeout(mqttRemainingTimeout(ctx)) {
		return fmt.Errorf("mqtt shared client connect timed out")
	}
	if err := connect.Error(); err != nil {
		return fmt.Errorf("mqtt shared client connect: %w", err)
	}
	defer client.Disconnect(250)

	messages := make(chan []byte, 16)
	subscribe := client.Subscribe(req.Topic, req.QoS, func(_ mqtt.Client, message mqtt.Message) {
		payload := append([]byte(nil), message.Payload()...)
		select {
		case messages <- payload:
		case <-ctx.Done():
		}
	})
	if !subscribe.WaitTimeout(mqttRemainingTimeout(ctx)) {
		return fmt.Errorf("mqtt shared client subscribe timed out")
	}
	if err := subscribe.Error(); err != nil {
		return fmt.Errorf("mqtt shared client subscribe: %w", err)
	}
	subscribeResult := mqttSubscribeResult(req.Topic, subscribe)

	publish := client.Publish(req.Topic, req.QoS, false, req.Payload)
	if !publish.WaitTimeout(mqttRemainingTimeout(ctx)) {
		return fmt.Errorf("mqtt shared client publish timed out")
	}
	if err := publish.Error(); err != nil {
		return fmt.Errorf("mqtt shared client publish: %w", err)
	}

	var lastErr error
	received := 0
	for {
		select {
		case payload := <-messages:
			received++
			if err := EvaluateMatchersBytes(req.MatchersFile, payload); err == nil {
				return nil
			} else {
				lastErr = err
			}
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("mqtt roundtrip expectation timed out on topic %q using shared client after receiving %d message(s); subscription %s: %w", req.Topic, received, subscribeResult, lastErr)
			}
			return fmt.Errorf("mqtt roundtrip expectation timed out on topic %q using shared client without receiving messages; subscription %s", req.Topic, subscribeResult)
		}
	}
}

func mqttSubscribeResult(topic string, token mqtt.Token) string {
	subscribeToken, ok := token.(*mqtt.SubscribeToken)
	if !ok {
		return "acknowledged with unknown grant"
	}
	result := subscribeToken.Result()
	if qos, ok := result[topic]; ok {
		if qos == 0x80 {
			return "rejected by broker"
		}
		return fmt.Sprintf("accepted with granted qos %d", qos)
	}
	if len(result) == 0 {
		return "acknowledged with no grant result"
	}
	return fmt.Sprintf("acknowledged with grants %v", result)
}

func mqttRemainingTimeout(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return time.Nanosecond
	}
	return remaining
}

func mqttClientOptions(brokerURL, clientID, username, password string, timeout time.Duration) *mqtt.ClientOptions {
	options := mqtt.NewClientOptions()
	options.AddBroker(brokerURL)
	options.SetClientID(clientID)
	options.SetConnectTimeout(timeout)
	options.SetWriteTimeout(timeout)
	options.SetAutoReconnect(false)
	options.SetCleanSession(true)
	if username != "" {
		options.SetUsername(username)
	}
	if password != "" {
		options.SetPassword(password)
	}
	return options
}

func mqttCredentialsFromEnv() (string, string) {
	return os.Getenv("SPEX_MQTT_USERNAME"), os.Getenv("SPEX_MQTT_PASSWORD")
}
