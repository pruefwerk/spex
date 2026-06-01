package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/pruefwerk/spex/internal/spex"
	"github.com/segmentio/kafka-go"
)

type reading struct {
	ScenarioRunID string      `json:"scenarioRunId"`
	CorrelationID string      `json:"correlationId"`
	TenantID      string      `json:"tenantId"`
	DeviceID      string      `json:"deviceId"`
	Measurement   string      `json:"measurement"`
	Value         json.Number `json:"value"`
	Unit          string      `json:"unit"`
}

type store struct {
	mu       sync.RWMutex
	readings map[string]reading
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		if err := spex.Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(spex.ExitCode(err))
		}
		return
	}
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: spex-demo-stack <ingest|graphql>")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "ingest":
		return runIngestor(ctx)
	case "graphql":
		return runGraphQL(ctx)
	default:
		return fmt.Errorf("unknown mode %q", args[0])
	}
}

func runIngestor(ctx context.Context) error {
	brokerURL := getenv("MQTT_BROKER_URL", "tcp://emqx.platform.svc.cluster.local:1883")
	topicFilter := getenv("MQTT_TOPIC_FILTER", "telemetry/+/+/readings")
	kafkaBrokers := splitCSV(getenv("REDPANDA_BROKERS", "redpanda.streaming.svc.cluster.local:9092"))
	kafkaTopic := getenv("REDPANDA_TOPIC", "ingestion.normalized-readings")
	if len(kafkaBrokers) == 0 {
		return fmt.Errorf("REDPANDA_BROKERS is required")
	}

	writer := &kafka.Writer{
		Addr:         kafka.TCP(kafkaBrokers...),
		Topic:        kafkaTopic,
		RequiredAcks: kafka.RequireOne,
		Balancer:     &kafka.Hash{},
	}
	defer writer.Close()

	options := mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID("spex-demo-stack-ingestor").
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(time.Second)
	if username := os.Getenv("MQTT_USERNAME"); username != "" {
		options.SetUsername(username)
	}
	if password := os.Getenv("MQTT_PASSWORD"); password != "" {
		options.SetPassword(password)
	}
	options.SetDefaultPublishHandler(func(_ mqtt.Client, msg mqtt.Message) {
		var event reading
		decoder := json.NewDecoder(strings.NewReader(string(msg.Payload())))
		decoder.UseNumber()
		if err := decoder.Decode(&event); err != nil {
			log.Printf("reject MQTT payload on %s: %v", msg.Topic(), err)
			return
		}
		if event.ScenarioRunID == "" || event.CorrelationID == "" {
			log.Printf("reject MQTT payload on %s: missing scenarioRunId or correlationId", msg.Topic())
			return
		}
		if err := writer.WriteMessages(context.Background(), kafka.Message{
			Key:   []byte(event.ScenarioRunID + "/" + event.CorrelationID),
			Value: msg.Payload(),
		}); err != nil {
			log.Printf("write Redpanda event: %v", err)
		}
	})

	client := mqtt.NewClient(options)
	if token := client.Connect(); !token.WaitTimeout(60 * time.Second) {
		return fmt.Errorf("MQTT connect timed out")
	} else if err := token.Error(); err != nil {
		return fmt.Errorf("MQTT connect: %w", err)
	}
	defer client.Disconnect(250)
	if token := client.Subscribe(topicFilter, 1, nil); !token.WaitTimeout(60 * time.Second) {
		return fmt.Errorf("MQTT subscribe timed out")
	} else if err := token.Error(); err != nil {
		return fmt.Errorf("MQTT subscribe: %w", err)
	}
	log.Printf("ingesting MQTT %s into Redpanda topic %s", topicFilter, kafkaTopic)
	<-ctx.Done()
	return nil
}

func runGraphQL(ctx context.Context) error {
	kafkaBrokers := splitCSV(getenv("REDPANDA_BROKERS", "redpanda.streaming.svc.cluster.local:9092"))
	kafkaTopic := getenv("REDPANDA_TOPIC", "ingestion.normalized-readings")
	address := getenv("GRAPHQL_ADDRESS", ":8080")
	requireAuth := getenv("GRAPHQL_REQUIRE_AUTH", "false") == "true"
	if len(kafkaBrokers) == 0 {
		return fmt.Errorf("REDPANDA_BROKERS is required")
	}
	state := &store{readings: map[string]reading{}}
	go consumeReadings(ctx, state, kafkaBrokers, kafkaTopic)

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		if requireAuth && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		scenarioRunID := stringVariable(request.Variables, "scenarioRunId")
		correlationID := stringVariable(request.Variables, "correlationId")
		deviceID := stringVariable(request.Variables, "deviceId")
		event, ok := state.find(scenarioRunID, correlationID, deviceID)
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"latestDeviceReading": nil},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"latestDeviceReading": event},
		})
	})
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("serving GraphQL on %s", address)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func consumeReadings(ctx context.Context, state *store, brokers []string, topic string) {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     brokers,
		Topic:       topic,
		GroupID:     "spex-demo-stack-graphql",
		StartOffset: kafka.FirstOffset,
		MinBytes:    1,
		MaxBytes:    10e6,
	})
	defer reader.Close()
	for {
		message, err := reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("fetch Redpanda event: %v", err)
			time.Sleep(time.Second)
			continue
		}
		var event reading
		decoder := json.NewDecoder(strings.NewReader(string(message.Value)))
		decoder.UseNumber()
		if err := decoder.Decode(&event); err != nil {
			log.Printf("skip Redpanda event at offset %d: %v", message.Offset, err)
			_ = reader.CommitMessages(ctx, message)
			continue
		}
		state.put(event)
		_ = reader.CommitMessages(ctx, message)
	}
}

func (s *store) put(event reading) {
	if event.ScenarioRunID == "" || event.CorrelationID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readings[key(event.ScenarioRunID, event.CorrelationID, event.DeviceID)] = event
}

func (s *store) find(scenarioRunID, correlationID, deviceID string) (reading, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	event, ok := s.readings[key(scenarioRunID, correlationID, deviceID)]
	return event, ok
}

func key(scenarioRunID, correlationID, deviceID string) string {
	return scenarioRunID + "\x00" + correlationID + "\x00" + deviceID
}

func stringVariable(values map[string]any, name string) string {
	if values == nil {
		return ""
	}
	value, ok := values[name]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func getenv(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
