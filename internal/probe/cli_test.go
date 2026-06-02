package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMQTTPublishStub(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "payload.json")
	if err := os.WriteFile(payload, []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeMQTTPublisher{}
	withMQTTPublisher(t, fake)
	t.Setenv("SPEX_MQTT_USERNAME", "user")
	t.Setenv("SPEX_MQTT_PASSWORD", "pass")
	var stdout bytes.Buffer
	if err := Run([]string{"mqtt", "publish", "--broker-url", "tcp://broker:1883", "--topic", "telemetry/readings", "--payload-file", payload, "--client-id", "client-1"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"operation":"mqtt.publish"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	if fake.request.BrokerURL != "tcp://broker:1883" || fake.request.Topic != "telemetry/readings" || fake.request.ClientID != "client-1" {
		t.Fatalf("unexpected request: %#v", fake.request)
	}
	if fake.request.QoS != 1 {
		t.Fatalf("unexpected QoS: %d", fake.request.QoS)
	}
	if fake.request.Username != "user" || fake.request.Password != "pass" {
		t.Fatalf("credentials not propagated: %#v", fake.request)
	}
	if string(fake.request.Payload) != `{"ok":true}` {
		t.Fatalf("payload not propagated: %s", string(fake.request.Payload))
	}
}

func TestMQTTPublishUsesBrokerURLFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	payload := writeTestFile(t, dir, "payload.json", `{"ok":true}`)
	fake := &fakeMQTTPublisher{}
	withMQTTPublisher(t, fake)
	t.Setenv("SPEX_MQTT_BROKER_URL", "tcp://broker-from-env:1883")

	var stdout bytes.Buffer
	err := Run([]string{"mqtt", "publish", "--topic", "telemetry/readings", "--payload-file", payload}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if fake.request.BrokerURL != "tcp://broker-from-env:1883" {
		t.Fatalf("unexpected broker URL: %#v", fake.request)
	}
}

func TestMQTTPublishAcceptsQoSOverride(t *testing.T) {
	dir := t.TempDir()
	payload := writeTestFile(t, dir, "payload.json", `{"ok":true}`)
	fake := &fakeMQTTPublisher{}
	withMQTTPublisher(t, fake)

	var stdout bytes.Buffer
	err := Run([]string{"mqtt", "publish", "--broker-url", "tcp://broker:1883", "--topic", "telemetry/readings", "--payload-file", payload, "--qos", "2"}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if fake.request.QoS != 2 {
		t.Fatalf("unexpected QoS: %d", fake.request.QoS)
	}
}

func TestMQTTPublishRejectsInvalidQoS(t *testing.T) {
	dir := t.TempDir()
	payload := writeTestFile(t, dir, "payload.json", `{"ok":true}`)
	withMQTTPublisher(t, &fakeMQTTPublisher{})

	var stdout bytes.Buffer
	err := Run([]string{"mqtt", "publish", "--broker-url", "tcp://broker:1883", "--topic", "telemetry/readings", "--payload-file", payload, "--qos", "3"}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--qos must be 0, 1, or 2") {
		t.Fatalf("expected QoS validation error, got %v", err)
	}
}

func TestMQTTPublishReportsPublisherFailure(t *testing.T) {
	dir := t.TempDir()
	payload := writeTestFile(t, dir, "payload.json", `{"ok":true}`)
	withMQTTPublisher(t, &fakeMQTTPublisher{err: fmt.Errorf("broker unavailable")})

	var stdout bytes.Buffer
	err := Run([]string{"mqtt", "publish", "--broker-url", "tcp://broker:1883", "--topic", "telemetry/readings", "--payload-file", payload}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "broker unavailable") {
		t.Fatalf("expected publisher error, got %v", err)
	}
	if !strings.Contains(stdout.String(), `"status":"failed"`) || !strings.Contains(stdout.String(), "broker unavailable") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"failureClass":"mqtt_publish_failed"`) {
		t.Fatalf("failure class missing from output: %s", stdout.String())
	}
}

func TestProbeRejectsUnexpectedPositionalArgs(t *testing.T) {
	tests := [][]string{
		{"mqtt", "publish", "extra"},
		{"rabbitmq", "publish", "extra"},
		{"rabbitmq", "expect", "extra"},
		{"redpanda", "snapshot-offsets", "extra"},
		{"redpanda", "contains", "extra"},
		{"graphql", "expect", "extra"},
	}
	for _, args := range tests {
		var stdout bytes.Buffer
		err := Run(args, &stdout, &bytes.Buffer{})
		if err == nil {
			t.Fatalf("Run(%v) succeeded; expected positional argument error", args)
		}
		if !strings.Contains(err.Error(), "does not accept positional arguments") {
			t.Fatalf("Run(%v) error mismatch: %v", args, err)
		}
		if stdout.Len() != 0 {
			t.Fatalf("Run(%v) wrote stdout:\n%s", args, stdout.String())
		}
	}
}

func TestRedpandaContainsStub(t *testing.T) {
	dir := t.TempDir()
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.correlationId","equalsString":"reading-1"}]`)
	offsets := writeTestFile(t, dir, "offsets.json", `{"topics":{"events":{"0":12}}}`)
	fake := &fakeRedpandaClient{}
	withRedpandaClient(t, fake)
	var stdout bytes.Buffer
	err := Run([]string{"redpanda", "contains", "--brokers", "redpanda:9092", "--topic", "events", "--offsets-file", offsets, "--matchers-file", matchers}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"operation":"redpanda.contains"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	if fake.containsTopic != "events" || fake.containsOffsets[0] != 12 || fake.containsMatchersFile != matchers {
		t.Fatalf("unexpected contains request: %#v", fake)
	}
}

func TestRedpandaContainsRejectsChangedPartitionSet(t *testing.T) {
	dir := t.TempDir()
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.correlationId","equalsString":"reading-1"}]`)
	offsets := writeTestFile(t, dir, "offsets.json", `{"topics":{"events":{"0":12}}}`)
	withRedpandaClient(t, &fakeRedpandaClient{
		partitions: []int{0, 1},
	})

	var stdout bytes.Buffer
	err := Run([]string{"redpanda", "contains", "--brokers", "redpanda:9092", "--topic", "events", "--offsets-file", offsets, "--matchers-file", matchers}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "redpanda_partition_set_changed") {
		t.Fatalf("expected partition set changed error, got %v", err)
	}
	if !strings.Contains(stdout.String(), `"status":"failed"`) || !strings.Contains(stdout.String(), "redpanda_partition_set_changed") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"failureClass":"redpanda_partition_set_changed"`) {
		t.Fatalf("failure class missing from output: %s", stdout.String())
	}
}

func TestRedpandaSnapshotOffsetsWritesFile(t *testing.T) {
	dir := t.TempDir()
	offsets := filepath.Join(dir, "offsets.json")
	fake := &fakeRedpandaClient{
		snapshotOffsets: map[string]map[int]int64{"events": {0: 10, 1: 20}},
	}
	withRedpandaClient(t, fake)

	var stdout bytes.Buffer
	err := Run([]string{"redpanda", "snapshot-offsets", "--brokers", "redpanda:9092", "--topic", "events", "--offsets-file", offsets, "--run-id", "run-1", "--timeout", "45s"}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"operation":"redpanda.snapshotOffsets"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	content, err := os.ReadFile(offsets)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"apiVersion": "spex.offsets.v0.1"`,
		`"scenarioRunId": "run-1"`,
		`"createdAt":`,
		`"events"`,
		`20`,
	} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("offsets missing %q: %s", want, string(content))
		}
	}
	if !strings.Contains(string(content), `"events"`) || !strings.Contains(string(content), `20`) {
		t.Fatalf("offsets were not written: %s", string(content))
	}
	if fake.snapshotDeadline.Before(time.Now().Add(40*time.Second)) || fake.snapshotDeadline.After(time.Now().Add(50*time.Second)) {
		t.Fatalf("snapshot deadline does not reflect --timeout: %s", fake.snapshotDeadline)
	}
}

func TestRedpandaSnapshotOffsetsRejectsInvalidTimeout(t *testing.T) {
	withRedpandaClient(t, &fakeRedpandaClient{})
	var stdout bytes.Buffer
	err := Run([]string{"redpanda", "snapshot-offsets", "--brokers", "redpanda:9092", "--topic", "events", "--offsets-file", filepath.Join(t.TempDir(), "offsets.json"), "--timeout", "0s"}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--timeout must be positive") {
		t.Fatalf("expected timeout validation error, got %v", err)
	}
}

func TestRedpandaConfigMapStoreLabelsRuntimeOffsets(t *testing.T) {
	store := kubernetesConfigMapOffsetsStore{
		name:      "spex-example-redpanda-offsets",
		namespace: "spex-test",
		scenario:  "mqtt-ingestion-basic",
		runID:     "run-1",
	}
	labels := store.labels()
	for key, want := range map[string]string{
		"spex/owned":    "true",
		"spex/runtime":  "true",
		"spex/scenario": "mqtt-ingestion-basic",
		"spex/run-id":   "run-1",
	} {
		if labels[key] != want {
			t.Fatalf("label %s = %q, want %q", key, labels[key], want)
		}
	}
}

func TestRedpandaContainsScansPartitionsConcurrently(t *testing.T) {
	previous := scanRedpandaPartition
	defer func() {
		scanRedpandaPartition = previous
	}()
	scanRedpandaPartition = func(ctx context.Context, brokers []string, topic string, partition int, offset int64, matchersFile string) error {
		if partition == 0 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := kafkaRedpandaClient{}.FindMatchingMessage(ctx, []string{"redpanda:9092"}, "events", map[int]int64{0: 10, 1: 20}, "matchers.json", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
}

func TestGraphQLExpectEvaluatesFixtureResponse(t *testing.T) {
	dir := t.TempDir()
	query := writeTestFile(t, dir, "query.graphql", `query { ok }`)
	variables := writeTestFile(t, dir, "variables.json", `{}`)
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.data.value","equalsNumber":"42.5"}]`)
	response := writeTestFile(t, dir, "response.json", `{"data":{"value":42.50}}`)

	var stdout bytes.Buffer
	err := Run([]string{
		"graphql", "expect",
		"--query-file", query,
		"--variables-file", variables,
		"--matchers-file", matchers,
		"--fixture-response-file", response,
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"status":"passed"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestRabbitMQExpectEvaluatesFixtureMessage(t *testing.T) {
	dir := t.TempDir()
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.correlationId","equalsString":"reading-1"},{"path":"$.value","equalsNumber":"42.5"}]`)
	message := writeTestFile(t, dir, "message.json", `{"correlationId":"reading-1","value":42.50}`)

	var stdout bytes.Buffer
	err := Run([]string{
		"rabbitmq", "expect",
		"--matchers-file", matchers,
		"--fixture-message-file", message,
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"operation":"rabbitmq.expect"`) || !strings.Contains(stdout.String(), `"status":"passed"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestRabbitMQExpectRejectsFixtureMismatch(t *testing.T) {
	dir := t.TempDir()
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.correlationId","equalsString":"reading-1"}]`)
	message := writeTestFile(t, dir, "message.json", `{"correlationId":"wrong"}`)

	var stdout bytes.Buffer
	err := Run([]string{
		"rabbitmq", "expect",
		"--matchers-file", matchers,
		"--fixture-message-file", message,
	}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "matcher") {
		t.Fatalf("expected matcher failure, got %v", err)
	}
	if !strings.Contains(stdout.String(), `"status":"failed"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"failureClass":"rabbitmq_expect_failed"`) {
		t.Fatalf("failure class missing from output: %s", stdout.String())
	}
}

func TestRabbitMQExpectRequiresLiveConnectionArgs(t *testing.T) {
	dir := t.TempDir()
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.correlationId","equalsString":"reading-1"}]`)

	var stdout bytes.Buffer
	err := Run([]string{
		"rabbitmq", "expect",
		"--matchers-file", matchers,
	}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires --uri and --queue") {
		t.Fatalf("expected RabbitMQ live arg validation error, got %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

func TestGraphQLExpectRejectsFixtureResponseErrors(t *testing.T) {
	dir := t.TempDir()
	query := writeTestFile(t, dir, "query.graphql", `query { ok }`)
	variables := writeTestFile(t, dir, "variables.json", `{}`)
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.data.value","equalsNumber":"42.5"}]`)
	response := writeTestFile(t, dir, "response.json", `{"errors":[{"message":"resolver failed"}],"data":{"value":42.5}}`)

	var stdout bytes.Buffer
	err := Run([]string{
		"graphql", "expect",
		"--query-file", query,
		"--variables-file", variables,
		"--matchers-file", matchers,
		"--fixture-response-file", response,
	}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "graphql response contains errors") {
		t.Fatalf("expected GraphQL errors failure, got %v", err)
	}
	if !strings.Contains(stdout.String(), `"status":"failed"`) || !strings.Contains(stdout.String(), "resolver failed") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"failureClass":"graphql_response_error"`) {
		t.Fatalf("failure class missing from output: %s", stdout.String())
	}
}

func TestGraphQLExpectPostsQueryVariablesAndToken(t *testing.T) {
	dir := t.TempDir()
	query := writeTestFile(t, dir, "query.graphql", `query Latest($deviceId: String!) { latestDeviceReading(deviceId: $deviceId) { value } }`)
	variables := writeTestFile(t, dir, "variables.json", `{"deviceId":"device-dev-1"}`)
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.data.latestDeviceReading.value","equalsNumber":"42.5"}]`)
	var sawBearer atomic.Bool
	withGraphQLRoundTripper(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "Bearer test-token" {
			sawBearer.Store(true)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if !strings.Contains(request["query"].(string), "latestDeviceReading") {
			t.Errorf("unexpected query: %v", request["query"])
		}
		variables := request["variables"].(map[string]any)
		if variables["deviceId"] != "device-dev-1" {
			t.Errorf("unexpected variables: %#v", variables)
		}
		return jsonResponse(`{"data":{"latestDeviceReading":{"value":42.50}}}`), nil
	}))
	t.Setenv("SPEX_GRAPHQL_TOKEN", "test-token")

	var stdout bytes.Buffer
	err := Run([]string{
		"graphql", "expect",
		"--endpoint", "http://graphql.test/query",
		"--query-file", query,
		"--variables-file", variables,
		"--matchers-file", matchers,
		"--timeout", "1s",
		"--poll-interval", "10ms",
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"status":"passed"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	if !sawBearer.Load() {
		t.Fatal("server did not receive bearer token")
	}
}

func TestMongoDBExpectEvaluatesFixtureDocument(t *testing.T) {
	dir := t.TempDir()
	filter := filepath.Join(dir, "filter.json")
	matchers := filepath.Join(dir, "matchers.json")
	document := filepath.Join(dir, "document.json")
	if err := os.WriteFile(filter, []byte(`{"scenarioRunId":"run-1","correlationId":"reading-1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(matchers, []byte(`[
{"path":"$.scenarioRunId","equalsString":"run-1"},
{"path":"$.correlationId","equalsString":"reading-1"},
{"path":"$.value","equalsNumber":"42.5"}
]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(document, []byte(`{"scenarioRunId":"run-1","correlationId":"reading-1","value":42.50}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	err := Run([]string{
		"mongodb", "expect",
		"--filter-file", filter,
		"--matchers-file", matchers,
		"--fixture-document-file", document,
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("mongodb fixture expect failed: %v", err)
	}
	if !strings.Contains(stdout.String(), `"operation":"mongodb.expect"`) || !strings.Contains(stdout.String(), `"status":"passed"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestMongoDBExpectRequiresConnectionFieldsWithoutFixture(t *testing.T) {
	dir := t.TempDir()
	filter := filepath.Join(dir, "filter.json")
	matchers := filepath.Join(dir, "matchers.json")
	if err := os.WriteFile(filter, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(matchers, []byte(`[{"path":"$","equalsNull":true}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Run([]string{
		"mongodb", "expect",
		"--filter-file", filter,
		"--matchers-file", matchers,
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "mongodb expect requires --uri, --database, and --collection") {
		t.Fatalf("expected missing connection fields error, got %v", err)
	}
}

func TestPostgreSQLExpectEvaluatesFixtureRow(t *testing.T) {
	dir := t.TempDir()
	query := filepath.Join(dir, "query.sql")
	args := filepath.Join(dir, "args.json")
	matchers := filepath.Join(dir, "matchers.json")
	row := filepath.Join(dir, "row.json")
	if err := os.WriteFile(query, []byte("select scenario_run_id, correlation_id, value from readings"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(args, []byte(`["run-1","reading-1"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(matchers, []byte(`[
{"path":"$.scenario_run_id","equalsString":"run-1"},
{"path":"$.correlation_id","equalsString":"reading-1"},
{"path":"$.value","equalsNumber":"42.5"}
]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(row, []byte(`{"scenario_run_id":"run-1","correlation_id":"reading-1","value":42.50}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	err := Run([]string{
		"postgresql", "expect",
		"--query-file", query,
		"--args-file", args,
		"--matchers-file", matchers,
		"--fixture-row-file", row,
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("postgresql fixture expect failed: %v", err)
	}
	if !strings.Contains(stdout.String(), `"operation":"postgresql.expect"`) || !strings.Contains(stdout.String(), `"status":"passed"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestPostgreSQLExpectRequiresURIWithoutFixture(t *testing.T) {
	dir := t.TempDir()
	query := filepath.Join(dir, "query.sql")
	args := filepath.Join(dir, "args.json")
	matchers := filepath.Join(dir, "matchers.json")
	if err := os.WriteFile(query, []byte("select 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(args, []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(matchers, []byte(`[{"path":"$","equalsNull":true}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Run([]string{
		"postgresql", "expect",
		"--query-file", query,
		"--args-file", args,
		"--matchers-file", matchers,
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "postgresql expect requires --uri") {
		t.Fatalf("expected missing URI error, got %v", err)
	}
}

func TestGraphQLExpectRejectsLiveResponseErrors(t *testing.T) {
	dir := t.TempDir()
	query := writeTestFile(t, dir, "query.graphql", `query { value }`)
	variables := writeTestFile(t, dir, "variables.json", `{}`)
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.data.value","equalsNumber":"42.5"}]`)
	withGraphQLRoundTripper(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"errors":[{"message":"resolver failed"}],"data":{"value":42.5}}`), nil
	}))

	var stdout bytes.Buffer
	err := Run([]string{
		"graphql", "expect",
		"--endpoint", "http://graphql.test/query",
		"--query-file", query,
		"--variables-file", variables,
		"--matchers-file", matchers,
		"--timeout", "20ms",
		"--poll-interval", "5ms",
	}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "graphql response contains errors") {
		t.Fatalf("expected GraphQL errors failure, got %v", err)
	}
	if !strings.Contains(stdout.String(), `"status":"failed"`) || !strings.Contains(stdout.String(), "resolver failed") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"failureClass":"graphql_response_error"`) {
		t.Fatalf("failure class missing from output: %s", stdout.String())
	}
}

func TestGraphQLExpectCancelsHungRequestAtTimeout(t *testing.T) {
	dir := t.TempDir()
	query := writeTestFile(t, dir, "query.graphql", `query { value }`)
	variables := writeTestFile(t, dir, "variables.json", `{}`)
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.data.value","equalsNumber":"42.5"}]`)
	var sawCanceled atomic.Bool
	withGraphQLRoundTripper(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		sawCanceled.Store(true)
		return nil, r.Context().Err()
	}))

	var stdout bytes.Buffer
	err := Run([]string{
		"graphql", "expect",
		"--endpoint", "http://graphql.test/query",
		"--query-file", query,
		"--variables-file", variables,
		"--matchers-file", matchers,
		"--timeout", "20ms",
		"--poll-interval", "5ms",
	}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected context deadline error, got %v", err)
	}
	if !sawCanceled.Load() {
		t.Fatal("GraphQL request context was not canceled")
	}
	if !strings.Contains(stdout.String(), `"status":"failed"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"failureClass":"graphql_match_timeout"`) {
		t.Fatalf("failure class missing from output: %s", stdout.String())
	}
}

func TestGraphQLExpectFetchesKeycloakClientCredentialsToken(t *testing.T) {
	dir := t.TempDir()
	query := writeTestFile(t, dir, "query.graphql", `query { value }`)
	variables := writeTestFile(t, dir, "variables.json", `{}`)
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.data.value","equalsNumber":"42.5"}]`)
	var sawTokenRequest atomic.Bool
	var sawGraphQLBearer atomic.Bool
	withGraphQLRoundTripper(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "http://keycloak.test/realms/dev/protocol/openid-connect/token":
			sawTokenRequest.Store(true)
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read token request: %v", err)
			}
			form, err := url.ParseQuery(string(body))
			if err != nil {
				t.Errorf("parse token form: %v", err)
			}
			if form.Get("grant_type") != "client_credentials" || form.Get("client_id") != "spex" || form.Get("client_secret") != "client-secret" || form.Get("scope") != "openid profile" {
				t.Errorf("unexpected token form: %s", string(body))
			}
			return jsonResponse(`{"access_token":"keycloak-token","token_type":"Bearer"}`), nil
		case "http://graphql.test/query":
			if r.Header.Get("Authorization") == "Bearer keycloak-token" {
				sawGraphQLBearer.Store(true)
			}
			return jsonResponse(`{"data":{"value":42.5}}`), nil
		default:
			t.Errorf("unexpected request URL: %s", r.URL.String())
			return jsonResponse(`{}`), nil
		}
	}))
	t.Setenv("SPEX_GRAPHQL_KEYCLOAK_CLIENT_SECRET", "client-secret")

	var stdout bytes.Buffer
	err := Run([]string{
		"graphql", "expect",
		"--endpoint", "http://graphql.test/query",
		"--query-file", query,
		"--variables-file", variables,
		"--matchers-file", matchers,
		"--keycloak-token-url", "http://keycloak.test/realms/dev/protocol/openid-connect/token",
		"--keycloak-client-id", "spex",
		"--keycloak-scope", "openid",
		"--keycloak-scope", "profile",
		"--timeout", "1s",
		"--poll-interval", "10ms",
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !sawTokenRequest.Load() {
		t.Fatal("Keycloak token endpoint was not called")
	}
	if !sawGraphQLBearer.Load() {
		t.Fatal("GraphQL endpoint did not receive Keycloak bearer token")
	}
}

func TestGraphQLExpectCancelsHungKeycloakTokenRequestAtTimeout(t *testing.T) {
	dir := t.TempDir()
	query := writeTestFile(t, dir, "query.graphql", `query { value }`)
	variables := writeTestFile(t, dir, "variables.json", `{}`)
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.data.value","equalsNumber":"42.5"}]`)
	var sawCanceled atomic.Bool
	withGraphQLRoundTripper(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		sawCanceled.Store(true)
		return nil, r.Context().Err()
	}))
	t.Setenv("SPEX_GRAPHQL_KEYCLOAK_CLIENT_SECRET", "client-secret")

	var stdout bytes.Buffer
	err := Run([]string{
		"graphql", "expect",
		"--endpoint", "http://graphql.test/query",
		"--query-file", query,
		"--variables-file", variables,
		"--matchers-file", matchers,
		"--keycloak-token-url", "http://keycloak.test/realms/dev/protocol/openid-connect/token",
		"--keycloak-client-id", "spex",
		"--timeout", "20ms",
		"--poll-interval", "5ms",
	}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected context deadline error, got %v", err)
	}
	if !sawCanceled.Load() {
		t.Fatal("Keycloak request context was not canceled")
	}
	if !strings.Contains(stdout.String(), `"status":"failed"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"failureClass":"graphql_match_timeout"`) {
		t.Fatalf("failure class missing from output: %s", stdout.String())
	}
}

func TestGraphQLExpectPollsUntilMatcherPasses(t *testing.T) {
	dir := t.TempDir()
	query := writeTestFile(t, dir, "query.graphql", `query { value }`)
	variables := writeTestFile(t, dir, "variables.json", `{}`)
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.data.value","equalsNumber":"42.5"}]`)
	var calls atomic.Int32
	withGraphQLRoundTripper(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return jsonResponse(`{"data":{"value":1}}`), nil
		}
		return jsonResponse(`{"data":{"value":42.5}}`), nil
	}))

	var stdout bytes.Buffer
	err := Run([]string{
		"graphql", "expect",
		"--endpoint", "http://graphql.test/query",
		"--query-file", query,
		"--variables-file", variables,
		"--matchers-file", matchers,
		"--timeout", "1s",
		"--poll-interval", "10ms",
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 2 {
		t.Fatalf("expected polling, got %d calls", calls.Load())
	}
}

func TestGraphQLExpectReportsMatcherFailure(t *testing.T) {
	dir := t.TempDir()
	query := writeTestFile(t, dir, "query.graphql", `query { value }`)
	variables := writeTestFile(t, dir, "variables.json", `{}`)
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.data.value","equalsNumber":"42.5"}]`)
	withGraphQLRoundTripper(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"data":{"value":1}}`), nil
	}))

	var stdout bytes.Buffer
	err := Run([]string{
		"graphql", "expect",
		"--endpoint", "http://graphql.test/query",
		"--query-file", query,
		"--variables-file", variables,
		"--matchers-file", matchers,
		"--timeout", "20ms",
		"--poll-interval", "5ms",
	}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "expected number 42.5") {
		t.Fatalf("expected matcher error, got %v", err)
	}
	if !strings.Contains(stdout.String(), `"status":"failed"`) || !strings.Contains(stdout.String(), "expected number 42.5") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"failureClass":"graphql_match_timeout"`) {
		t.Fatalf("failure class missing from output: %s", stdout.String())
	}
}

func TestRedpandaContainsEvaluatesFixtureEvent(t *testing.T) {
	dir := t.TempDir()
	matchers := writeTestFile(t, dir, "matchers.json", `[{"path":"$.correlationId","equalsString":"reading-1"}]`)
	event := writeTestFile(t, dir, "event.json", `{"correlationId":"wrong"}`)

	var stdout bytes.Buffer
	err := Run([]string{
		"redpanda", "contains",
		"--offsets-configmap", "spex-offsets",
		"--matchers-file", matchers,
		"--fixture-event-file", event,
	}, &stdout, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "expected string") {
		t.Fatalf("expected matcher error, got %v", err)
	}
	if !strings.Contains(stdout.String(), `"status":"failed"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func withGraphQLRoundTripper(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	previous := graphQLHTTPClient
	graphQLHTTPClient = &http.Client{Transport: transport}
	t.Cleanup(func() {
		graphQLHTTPClient = previous
	})
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type fakeMQTTPublisher struct {
	request mqttPublishRequest
	err     error
}

func (f *fakeMQTTPublisher) Publish(req mqttPublishRequest) error {
	f.request = req
	return f.err
}

func withMQTTPublisher(t *testing.T, publisher mqttPublisher) {
	t.Helper()
	previous := mqttClient
	mqttClient = publisher
	t.Cleanup(func() {
		mqttClient = previous
	})
}

type fakeRedpandaClient struct {
	snapshotOffsets      map[string]map[int]int64
	snapshotErr          error
	snapshotDeadline     time.Time
	partitions           []int
	partitionsErr        error
	containsTopic        string
	containsOffsets      map[int]int64
	containsMatchersFile string
	containsErr          error
}

func (f *fakeRedpandaClient) SnapshotOffsets(ctx context.Context, brokers []string, topics []string) (map[string]map[int]int64, error) {
	if deadline, ok := ctx.Deadline(); ok {
		f.snapshotDeadline = deadline
	}
	if f.snapshotOffsets != nil || f.snapshotErr != nil {
		return f.snapshotOffsets, f.snapshotErr
	}
	out := map[string]map[int]int64{}
	for _, topic := range topics {
		out[topic] = map[int]int64{0: 0}
	}
	return out, nil
}

func (f *fakeRedpandaClient) Partitions(ctx context.Context, brokers []string, topic string) ([]int, error) {
	if f.partitions != nil || f.partitionsErr != nil {
		return f.partitions, f.partitionsErr
	}
	return []int{0}, nil
}

func (f *fakeRedpandaClient) FindMatchingMessage(ctx context.Context, brokers []string, topic string, offsets map[int]int64, matchersFile string, pollInterval time.Duration) error {
	f.containsTopic = topic
	f.containsOffsets = offsets
	f.containsMatchersFile = matchersFile
	return f.containsErr
}

func withRedpandaClient(t *testing.T, client redpandaClient) {
	t.Helper()
	previous := redpandaKafka
	redpandaKafka = client
	t.Cleanup(func() {
		redpandaKafka = previous
	})
}
