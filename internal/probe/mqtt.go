package probe

import (
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

type mqttPublisher interface {
	Publish(mqttPublishRequest) error
}

type pahoMQTTPublisher struct{}

var mqttClient mqttPublisher = pahoMQTTPublisher{}

func publishMQTT(req mqttPublishRequest) error {
	return mqttClient.Publish(req)
}

func (pahoMQTTPublisher) Publish(req mqttPublishRequest) error {
	options := mqtt.NewClientOptions()
	options.AddBroker(req.BrokerURL)
	options.SetClientID(req.ClientID)
	options.SetConnectTimeout(req.Timeout)
	options.SetWriteTimeout(req.Timeout)
	options.SetAutoReconnect(false)
	options.SetCleanSession(true)
	if req.Username != "" {
		options.SetUsername(req.Username)
	}
	if req.Password != "" {
		options.SetPassword(req.Password)
	}

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

func mqttCredentialsFromEnv() (string, string) {
	return os.Getenv("SPEX_MQTT_USERNAME"), os.Getenv("SPEX_MQTT_PASSWORD")
}
