package probe

import (
	"context"
	"fmt"
	"os"
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
				return fmt.Errorf("mqtt roundtrip expectation timed out on topic %q after receiving %d message(s); subscription %s: %w", req.Topic, received, subscribeResult, lastErr)
			}
			return fmt.Errorf("mqtt roundtrip expectation timed out on topic %q without receiving messages; subscription %s", req.Topic, subscribeResult)
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
