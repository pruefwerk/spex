package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
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

	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"
	"github.com/segmentio/kafka-go/sasl/scram"
)

const offsetsDataKey = "offsets.json"

type redpandaOffsetSnapshot struct {
	APIVersion    string                   `json:"apiVersion"`
	ScenarioRunID string                   `json:"scenarioRunId,omitempty"`
	CreatedAt     string                   `json:"createdAt,omitempty"`
	Topics        map[string]map[int]int64 `json:"topics"`
}

type redpandaClient interface {
	Ping(ctx context.Context, req redpandaPingRequest) error
	SnapshotOffsets(ctx context.Context, brokers []string, topics []string) (map[string]map[int]int64, error)
	Partitions(ctx context.Context, brokers []string, topic string) ([]int, error)
	FindMatchingMessage(ctx context.Context, brokers []string, topic string, offsets map[int]int64, matchersFile string, pollInterval time.Duration) error
}

type redpandaPingRequest struct {
	Brokers          []string
	Topic            string
	SecurityProtocol string
	SASLMechanism    string
	Username         string
	Password         string
	CACertB64        string
}

type redpandaOffsetsStore interface {
	Save(redpandaOffsetSnapshot) error
	Load() (redpandaOffsetSnapshot, error)
}

type kafkaRedpandaClient struct{}

var redpandaKafka redpandaClient = kafkaRedpandaClient{}
var scanRedpandaPartition = scanPartition

func runRedpandaOperation(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("redpanda run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	operationFile := fs.String("operation-file", "", "lowered operation JSON file")
	resultFile := fs.String("result-file", "", "normalized result envelope path")
	timeoutValue := fs.String("timeout", "", "timeout override")
	pollIntervalValue := fs.String("poll-interval", "1s", "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "redpanda run"); err != nil {
		return err
	}
	if *operationFile == "" || *resultFile == "" {
		return fmt.Errorf("redpanda run requires --operation-file and --result-file")
	}
	operation, err := readLoweredOperation(*operationFile)
	if err != nil {
		return err
	}
	if operation.Provider != "redpanda" || (operation.OperationType != "redpanda.ping" && operation.OperationType != "redpanda.contains" && operation.OperationType != "redpanda.snapshotOffsets") {
		return fmt.Errorf("redpanda run cannot execute operation type %q from provider %q", operation.OperationType, operation.Provider)
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
	err = executeRedpandaLoweredOperation(operation, timeout, pollInterval)
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

func executeRedpandaLoweredOperation(operation probeLoweredOperation, timeout, pollInterval time.Duration) error {
	if operation.OperationType == "redpanda.ping" {
		return executeRedpandaPingLoweredOperation(operation, timeout)
	}
	if operation.OperationType == "redpanda.snapshotOffsets" {
		return executeRedpandaSnapshotLoweredOperation(operation, timeout)
	}
	dir, err := os.MkdirTemp("", "spex-redpanda-*")
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
	brokers, _ := operation.Binding.With["brokers"].(string)
	if brokersFromEnv := os.Getenv("SPEX_REDPANDA_BROKERS"); brokersFromEnv != "" && redpandaBrokersUseRuntimeEnv(brokers) {
		brokers = brokersFromEnv
	}
	topic, _ := operation.With["topic"].(string)
	offsetsConfigMap, _ := operation.With["offsetsConfigMap"].(string)
	namespace, _ := operation.With["namespace"].(string)
	scenario, _ := operation.With["scenario"].(string)
	runID, _ := operation.With["runId"].(string)
	store := redpandaOffsetStore(offsetsConfigMap, "", namespace, scenario, runID)
	snapshot, err := store.Load()
	if err != nil {
		return err
	}
	return redpandaContains(brokers, topic, snapshot.Topics[topic], matchersFile, timeout, pollInterval)
}

func redpandaBrokersUseRuntimeEnv(brokers string) bool {
	trimmed := strings.TrimSpace(brokers)
	return trimmed == "" || strings.HasPrefix(trimmed, "{{ ssm ")
}

func executeRedpandaPingLoweredOperation(operation probeLoweredOperation, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	brokers, _ := operation.Binding.With["brokers"].(string)
	topic, _ := operation.With["topic"].(string)
	securityProtocol, _ := operation.Binding.With["securityProtocol"].(string)
	saslMechanism, _ := operation.Binding.With["saslMechanism"].(string)
	return redpandaKafka.Ping(ctx, redpandaPingRequest{
		Brokers:          splitBrokers(brokers),
		Topic:            topic,
		SecurityProtocol: securityProtocol,
		SASLMechanism:    saslMechanism,
		Username:         os.Getenv("SPEX_REDPANDA_USERNAME"),
		Password:         os.Getenv("SPEX_REDPANDA_PASSWORD"),
		CACertB64:        os.Getenv("SPEX_REDPANDA_CA_CRT_B64"),
	})
}

func executeRedpandaSnapshotLoweredOperation(operation probeLoweredOperation, timeout time.Duration) error {
	brokers, _ := operation.Binding.With["brokers"].(string)
	topics := loweredStringSlice(operation.With["topics"])
	offsetsConfigMap, _ := operation.With["offsetsConfigMap"].(string)
	offsetsFile, _ := operation.With["offsetsFile"].(string)
	namespace, _ := operation.With["namespace"].(string)
	scenario, _ := operation.With["scenario"].(string)
	runID, _ := operation.With["runId"].(string)
	offsets, err := snapshotRedpandaOffsets(brokers, topics, timeout)
	if err != nil {
		return err
	}
	store := redpandaOffsetStore(offsetsConfigMap, offsetsFile, namespace, scenario, runID)
	return store.Save(redpandaOffsetSnapshot{
		APIVersion:    "spex.offsets.v0.1",
		ScenarioRunID: runID,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		Topics:        offsets,
	})
}

func snapshotRedpandaOffsets(brokersValue string, topics []string, timeout time.Duration) (map[string]map[int]int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return redpandaKafka.SnapshotOffsets(ctx, splitBrokers(brokersValue), topics)
}

func redpandaContains(brokersValue, topic string, offsets map[int]int64, matchersFile string, timeout, pollInterval time.Duration) error {
	if len(offsets) == 0 {
		return fmt.Errorf("offset snapshot does not contain topic %q", topic)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	brokers := splitBrokers(brokersValue)
	partitions, err := redpandaKafka.Partitions(ctx, brokers, topic)
	if err != nil {
		return err
	}
	if err := validateRedpandaPartitionSet(topic, offsets, partitions); err != nil {
		return err
	}
	return redpandaKafka.FindMatchingMessage(ctx, brokers, topic, offsets, matchersFile, pollInterval)
}

func (kafkaRedpandaClient) SnapshotOffsets(ctx context.Context, brokers []string, topics []string) (map[string]map[int]int64, error) {
	if len(brokers) == 0 {
		return nil, fmt.Errorf("no Redpanda brokers configured")
	}
	out := make(map[string]map[int]int64, len(topics))
	for _, topic := range topics {
		topic = strings.TrimSpace(topic)
		if topic == "" {
			continue
		}
		partitions, err := kafka.LookupPartitions(ctx, "tcp", brokers[0], topic)
		if err != nil {
			return nil, fmt.Errorf("lookup partitions for %q: %w", topic, err)
		}
		out[topic] = map[int]int64{}
		for _, partition := range partitions {
			conn, err := kafka.DialLeader(ctx, "tcp", brokers[0], topic, partition.ID)
			if err != nil {
				return nil, fmt.Errorf("dial leader for %q partition %d: %w", topic, partition.ID, err)
			}
			offset, readErr := conn.ReadLastOffset()
			closeErr := conn.Close()
			if readErr != nil {
				return nil, fmt.Errorf("read last offset for %q partition %d: %w", topic, partition.ID, readErr)
			}
			if closeErr != nil {
				return nil, fmt.Errorf("close leader connection for %q partition %d: %w", topic, partition.ID, closeErr)
			}
			out[topic][partition.ID] = offset
		}
	}
	return out, nil
}

func (kafkaRedpandaClient) Ping(ctx context.Context, req redpandaPingRequest) error {
	if len(req.Brokers) == 0 {
		return fmt.Errorf("no Redpanda brokers configured")
	}
	dialer, err := redpandaDialer(req)
	if err != nil {
		return err
	}
	if req.Topic != "" {
		_, err := dialer.LookupPartitions(ctx, "tcp", req.Brokers[0], req.Topic)
		if err != nil {
			return fmt.Errorf("lookup partitions for %q: %w", req.Topic, err)
		}
		return nil
	}
	conn, err := dialer.DialContext(ctx, "tcp", req.Brokers[0])
	if err != nil {
		return err
	}
	return conn.Close()
}

func redpandaDialer(req redpandaPingRequest) (*kafka.Dialer, error) {
	dialer := &kafka.Dialer{Timeout: 10 * time.Second}
	if strings.EqualFold(req.SecurityProtocol, "SASL_SSL") {
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
		if req.CACertB64 != "" {
			decoded, err := base64.StdEncoding.DecodeString(req.CACertB64)
			if err != nil {
				return nil, fmt.Errorf("redpanda CA certificate base64: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(decoded) {
				return nil, fmt.Errorf("redpanda CA certificate contains no PEM certificates")
			}
			tlsConfig.RootCAs = pool
		}
		dialer.TLS = tlsConfig
		mechanism, err := redpandaSASLMechanism(req)
		if err != nil {
			return nil, err
		}
		dialer.SASLMechanism = mechanism
	}
	return dialer, nil
}

func redpandaSASLMechanism(req redpandaPingRequest) (sasl.Mechanism, error) {
	if req.Username == "" || req.Password == "" {
		return nil, fmt.Errorf("redpanda SASL username and password are required")
	}
	switch req.SASLMechanism {
	case "", "SCRAM-SHA-512":
		return scram.Mechanism(scram.SHA512, req.Username, req.Password)
	case "SCRAM-SHA-256":
		return scram.Mechanism(scram.SHA256, req.Username, req.Password)
	default:
		return nil, fmt.Errorf("unsupported Redpanda SASL mechanism %q", req.SASLMechanism)
	}
}

func (kafkaRedpandaClient) Partitions(ctx context.Context, brokers []string, topic string) ([]int, error) {
	if len(brokers) == 0 {
		return nil, fmt.Errorf("no Redpanda brokers configured")
	}
	partitions, err := kafka.LookupPartitions(ctx, "tcp", brokers[0], topic)
	if err != nil {
		return nil, fmt.Errorf("lookup partitions for %q: %w", topic, err)
	}
	out := make([]int, 0, len(partitions))
	for _, partition := range partitions {
		out = append(out, partition.ID)
	}
	return out, nil
}

func validateRedpandaPartitionSet(topic string, offsets map[int]int64, partitions []int) error {
	current := map[int]struct{}{}
	for _, partition := range partitions {
		current[partition] = struct{}{}
	}
	for partition := range offsets {
		if _, ok := current[partition]; !ok {
			return fmt.Errorf("redpanda_partition_set_changed: topic %q snapshot contains partition %d but current partition set does not", topic, partition)
		}
	}
	for partition := range current {
		if _, ok := offsets[partition]; !ok {
			return fmt.Errorf("redpanda_partition_set_changed: topic %q current partition set contains partition %d but snapshot does not", topic, partition)
		}
	}
	return nil
}

func (kafkaRedpandaClient) FindMatchingMessage(ctx context.Context, brokers []string, topic string, offsets map[int]int64, matchersFile string, pollInterval time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type scanResult struct {
		partition int
		err       error
	}
	results := make(chan scanResult, len(offsets))
	for partition, offset := range offsets {
		go func(partition int, offset int64) {
			results <- scanResult{
				partition: partition,
				err:       scanRedpandaPartition(ctx, brokers, topic, partition, offset, matchersFile),
			}
		}(partition, offset)
	}

	var lastErr error
	remaining := len(offsets)
	for remaining > 0 {
		select {
		case result := <-results:
			remaining--
			if result.err == nil {
				return nil
			}
			lastErr = fmt.Errorf("partition %d: %w", result.partition, result.err)
		case <-ctx.Done():
			remaining = 0
		}
	}
	if lastErr != nil {
		return fmt.Errorf("matching Redpanda event not found before timeout: %w", lastErr)
	}
	return fmt.Errorf("matching Redpanda event not found before timeout")
}

func scanPartition(ctx context.Context, brokers []string, topic string, partition int, offset int64, matchersFile string) error {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:   brokers,
		Topic:     topic,
		Partition: partition,
		MinBytes:  1,
		MaxBytes:  10e6,
	})
	defer reader.Close()
	if err := reader.SetOffset(offset); err != nil {
		return fmt.Errorf("set offset for %q partition %d: %w", topic, partition, err)
	}
	for {
		message, err := reader.FetchMessage(ctx)
		if err != nil {
			return err
		}
		if err := EvaluateMatchersBytes(matchersFile, message.Value); err == nil {
			return nil
		}
	}
}

func redpandaOffsetStore(configMapName, offsetsFile, namespace, scenario, runID string) redpandaOffsetsStore {
	if offsetsFile != "" {
		return fileOffsetsStore{path: offsetsFile}
	}
	return kubernetesConfigMapOffsetsStore{name: configMapName, namespace: namespace, scenario: scenario, runID: runID}
}

type fileOffsetsStore struct {
	path string
}

func (s fileOffsetsStore) Save(snapshot redpandaOffsetSnapshot) error {
	content, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, append(content, '\n'), 0o644)
}

func (s fileOffsetsStore) Load() (redpandaOffsetSnapshot, error) {
	content, err := os.ReadFile(s.path)
	if err != nil {
		return redpandaOffsetSnapshot{}, err
	}
	var snapshot redpandaOffsetSnapshot
	if err := json.Unmarshal(content, &snapshot); err != nil {
		return redpandaOffsetSnapshot{}, err
	}
	return snapshot, nil
}

type kubernetesConfigMapOffsetsStore struct {
	name      string
	namespace string
	scenario  string
	runID     string
}

func (s kubernetesConfigMapOffsetsStore) Save(snapshot redpandaOffsetSnapshot) error {
	content, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	namespace, err := s.resolvedNamespace()
	if err != nil {
		return err
	}
	client, err := inClusterKubernetesClient()
	if err != nil {
		return err
	}
	body := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      s.name,
			"namespace": namespace,
			"labels":    s.labels(),
		},
		"data": map[string]string{
			offsetsDataKey: string(content),
		},
	}
	if err := client.createConfigMap(namespace, s.name, body); err == nil {
		return nil
	} else if !isKubernetesNotFoundOrAlreadyExists(err) {
		return err
	}
	patch := map[string]any{
		"data": map[string]string{offsetsDataKey: string(content)},
	}
	if labels := s.labels(); len(labels) > 0 {
		patch["metadata"] = map[string]any{"labels": labels}
	}
	return client.patchConfigMap(namespace, s.name, patch)
}

func (s kubernetesConfigMapOffsetsStore) labels() map[string]string {
	labels := map[string]string{
		"spex/owned":   "true",
		"spex/runtime": "true",
	}
	if s.scenario != "" {
		labels["spex/scenario"] = s.scenario
	}
	if s.runID != "" {
		labels["spex/run-id"] = s.runID
	}
	return labels
}

func (s kubernetesConfigMapOffsetsStore) Load() (redpandaOffsetSnapshot, error) {
	namespace, err := s.resolvedNamespace()
	if err != nil {
		return redpandaOffsetSnapshot{}, err
	}
	client, err := inClusterKubernetesClient()
	if err != nil {
		return redpandaOffsetSnapshot{}, err
	}
	configMap, err := client.getConfigMap(namespace, s.name)
	if err != nil {
		return redpandaOffsetSnapshot{}, err
	}
	raw, ok := configMap.Data[offsetsDataKey]
	if !ok {
		return redpandaOffsetSnapshot{}, fmt.Errorf("ConfigMap %q missing %s", s.name, offsetsDataKey)
	}
	var snapshot redpandaOffsetSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return redpandaOffsetSnapshot{}, err
	}
	return snapshot, nil
}

func (s kubernetesConfigMapOffsetsStore) resolvedNamespace() (string, error) {
	if s.namespace != "" {
		return s.namespace, nil
	}
	content, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", fmt.Errorf("read service account namespace: %w", err)
	}
	return strings.TrimSpace(string(content)), nil
}

type kubernetesClient struct {
	baseURL string
	token   string
	http    *http.Client
}

type configMapResponse struct {
	Data map[string]string `json:"data"`
}

func inClusterKubernetesClient() (kubernetesClient, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return kubernetesClient{}, fmt.Errorf("KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT are required for ConfigMap offset storage")
	}
	token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return kubernetesClient{}, fmt.Errorf("read service account token: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	caPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	if ca, err := os.ReadFile(caPath); err == nil {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(ca) {
			transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
	}
	return kubernetesClient{
		baseURL: "https://" + host + ":" + port,
		token:   strings.TrimSpace(string(token)),
		http:    &http.Client{Transport: transport, Timeout: 10 * time.Second},
	}, nil
}

func (c kubernetesClient) getConfigMap(namespace, name string) (configMapResponse, error) {
	var out configMapResponse
	err := c.doJSON(http.MethodGet, configMapPath(namespace, name), nil, "", &out)
	return out, err
}

func (c kubernetesClient) createConfigMap(namespace, name string, body map[string]any) error {
	return c.doJSON(http.MethodPost, configMapsPath(namespace), body, "application/json", nil)
}

func (c kubernetesClient) patchConfigMap(namespace, name string, patch map[string]any) error {
	return c.doJSON(http.MethodPatch, configMapPath(namespace, name), patch, "application/merge-patch+json", nil)
}

func (c kubernetesClient) doJSON(method, path string, body any, contentType string, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return kubernetesStatusError{code: resp.StatusCode}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type kubernetesStatusError struct {
	code int
}

func (e kubernetesStatusError) Error() string {
	return "Kubernetes API returned HTTP " + strconv.Itoa(e.code)
}

func isKubernetesNotFoundOrAlreadyExists(err error) bool {
	status, ok := err.(kubernetesStatusError)
	return ok && (status.code == http.StatusNotFound || status.code == http.StatusConflict)
}

func configMapsPath(namespace string) string {
	return "/api/v1/namespaces/" + namespace + "/configmaps"
}

func configMapPath(namespace, name string) string {
	return filepath.ToSlash(configMapsPath(namespace) + "/" + name)
}

func splitBrokers(value string) []string {
	var brokers []string
	for _, broker := range strings.Split(value, ",") {
		broker = strings.TrimSpace(broker)
		if broker != "" {
			brokers = append(brokers, broker)
		}
	}
	return brokers
}
