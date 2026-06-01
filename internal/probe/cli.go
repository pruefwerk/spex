package probe

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func Run(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: spex-probe <graphql|mongodb|mqtt|postgresql|rabbitmq|redpanda> <subcommand>")
	}
	switch args[0] + " " + args[1] {
	case "graphql expect":
		return runGraphQLExpect(args[2:], stdout)
	case "mongodb expect":
		return runMongoDBExpect(args[2:], stdout)
	case "mqtt publish":
		return runMQTTPublish(args[2:], stdout)
	case "postgresql expect":
		return runPostgreSQLExpect(args[2:], stdout)
	case "rabbitmq publish":
		return runRabbitMQPublish(args[2:], stdout)
	case "rabbitmq expect":
		return runRabbitMQExpect(args[2:], stdout)
	case "redpanda snapshot-offsets":
		return runRedpandaSnapshotOffsets(args[2:], stdout)
	case "redpanda contains":
		return runRedpandaContains(args[2:], stdout)
	default:
		return fmt.Errorf("unknown probe command %q %q", args[0], args[1])
	}
}

func runMongoDBExpect(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("mongodb expect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	uri := fs.String("uri", "", "MongoDB URI")
	database := fs.String("database", "", "MongoDB database")
	collection := fs.String("collection", "", "MongoDB collection")
	filterFile := fs.String("filter-file", "", "MongoDB filter JSON file")
	matchersFile := fs.String("matchers-file", "", "matchers JSON file")
	fixtureDocumentFile := fs.String("fixture-document-file", "", "fixture document JSON file")
	timeoutValue := fs.String("timeout", "30s", "timeout")
	pollIntervalValue := fs.String("poll-interval", "1s", "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "mongodb expect"); err != nil {
		return err
	}
	if *filterFile == "" || *matchersFile == "" {
		return fmt.Errorf("mongodb expect requires --filter-file and --matchers-file")
	}
	for _, path := range []string{*filterFile, *matchersFile} {
		if _, err := os.Stat(path); err != nil {
			return err
		}
	}
	if *fixtureDocumentFile != "" {
		if err := EvaluateMatchersFile(*matchersFile, *fixtureDocumentFile); err != nil {
			return emitFailure(stdout, "mongodb.expect", err)
		}
		return emit(stdout, "mongodb.expect", "passed", "")
	}
	if *uri == "" || *database == "" || *collection == "" {
		return fmt.Errorf("mongodb expect requires --uri, --database, and --collection unless --fixture-document-file is used")
	}
	timeout, err := time.ParseDuration(*timeoutValue)
	if err != nil {
		return fmt.Errorf("invalid --timeout: %w", err)
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
	username, password := mongoDBCredentialsFromEnv()
	if err := expectMongoDB(mongoDBExpectRequest{
		URI:          *uri,
		Database:     *database,
		Collection:   *collection,
		Username:     username,
		Password:     password,
		FilterFile:   *filterFile,
		MatchersFile: *matchersFile,
		Timeout:      timeout,
		PollInterval: pollInterval,
	}); err != nil {
		return emitFailure(stdout, "mongodb.expect", err)
	}
	return emit(stdout, "mongodb.expect", "passed", "")
}

func runPostgreSQLExpect(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("postgresql expect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	uri := fs.String("uri", "", "PostgreSQL URI")
	queryFile := fs.String("query-file", "", "SQL query file")
	argsFile := fs.String("args-file", "", "SQL args JSON file")
	matchersFile := fs.String("matchers-file", "", "matchers JSON file")
	fixtureRowFile := fs.String("fixture-row-file", "", "fixture row JSON file")
	timeoutValue := fs.String("timeout", "30s", "timeout")
	pollIntervalValue := fs.String("poll-interval", "1s", "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "postgresql expect"); err != nil {
		return err
	}
	for _, path := range []string{*queryFile, *argsFile, *matchersFile} {
		if path == "" {
			return fmt.Errorf("postgresql expect requires --query-file, --args-file, and --matchers-file")
		}
		if _, err := os.Stat(path); err != nil {
			return err
		}
	}
	if *fixtureRowFile != "" {
		if err := EvaluateMatchersFile(*matchersFile, *fixtureRowFile); err != nil {
			return emitFailure(stdout, "postgresql.expect", err)
		}
		return emit(stdout, "postgresql.expect", "passed", "")
	}
	if *uri == "" {
		return fmt.Errorf("postgresql expect requires --uri unless --fixture-row-file is used")
	}
	timeout, err := time.ParseDuration(*timeoutValue)
	if err != nil {
		return fmt.Errorf("invalid --timeout: %w", err)
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
	username, password := postgreSQLCredentialsFromEnv()
	if err := expectPostgreSQL(postgreSQLExpectRequest{
		URI:          *uri,
		Username:     username,
		Password:     password,
		QueryFile:    *queryFile,
		ArgsFile:     *argsFile,
		MatchersFile: *matchersFile,
		Timeout:      timeout,
		PollInterval: pollInterval,
	}); err != nil {
		return emitFailure(stdout, "postgresql.expect", err)
	}
	return emit(stdout, "postgresql.expect", "passed", "")
}

func runRabbitMQPublish(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("rabbitmq publish", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	payloadFile := fs.String("payload-file", "", "payload JSON file")
	uri := fs.String("uri", "", "RabbitMQ URI")
	exchange := fs.String("exchange", "", "RabbitMQ exchange")
	routingKey := fs.String("routing-key", "", "RabbitMQ routing key")
	timeoutValue := fs.String("timeout", "30s", "timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "rabbitmq publish"); err != nil {
		return err
	}
	if *payloadFile == "" || *uri == "" || *routingKey == "" {
		return fmt.Errorf("rabbitmq publish requires --uri, --routing-key, and --payload-file")
	}
	payload, err := os.ReadFile(*payloadFile)
	if err != nil {
		return err
	}
	timeout, err := time.ParseDuration(*timeoutValue)
	if err != nil {
		return fmt.Errorf("invalid --timeout: %w", err)
	}
	if timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	username, password := rabbitMQCredentialsFromEnv()
	if err := publishRabbitMQ(rabbitMQPublishRequest{
		URI:        *uri,
		Exchange:   *exchange,
		RoutingKey: *routingKey,
		Username:   username,
		Password:   password,
		Payload:    payload,
		Timeout:    timeout,
	}); err != nil {
		return emitFailure(stdout, "rabbitmq.publish", err)
	}
	return emit(stdout, "rabbitmq.publish", "passed", "")
}

func runRabbitMQExpect(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("rabbitmq expect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	uri := fs.String("uri", "", "RabbitMQ URI")
	queue := fs.String("queue", "", "RabbitMQ queue")
	matchersFile := fs.String("matchers-file", "", "matchers JSON file")
	fixtureMessageFile := fs.String("fixture-message-file", "", "fixture message JSON file")
	timeoutValue := fs.String("timeout", "30s", "timeout")
	pollIntervalValue := fs.String("poll-interval", "1s", "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "rabbitmq expect"); err != nil {
		return err
	}
	if *matchersFile == "" {
		return fmt.Errorf("rabbitmq expect requires --matchers-file")
	}
	if _, err := os.Stat(*matchersFile); err != nil {
		return err
	}
	if *fixtureMessageFile != "" {
		if err := EvaluateMatchersFile(*matchersFile, *fixtureMessageFile); err != nil {
			return emitFailure(stdout, "rabbitmq.expect", err)
		}
		return emit(stdout, "rabbitmq.expect", "passed", "")
	}
	if *uri == "" || *queue == "" {
		return fmt.Errorf("rabbitmq expect requires --uri and --queue unless --fixture-message-file is used")
	}
	timeout, err := time.ParseDuration(*timeoutValue)
	if err != nil {
		return fmt.Errorf("invalid --timeout: %w", err)
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
	username, password := rabbitMQCredentialsFromEnv()
	if err := expectRabbitMQ(rabbitMQExpectRequest{
		URI:          *uri,
		Queue:        *queue,
		Username:     username,
		Password:     password,
		MatchersFile: *matchersFile,
		Timeout:      timeout,
		PollInterval: pollInterval,
	}); err != nil {
		return emitFailure(stdout, "rabbitmq.expect", err)
	}
	return emit(stdout, "rabbitmq.expect", "passed", "")
}

func rejectProbePositionalArgs(fs *flag.FlagSet, command string) error {
	if fs.NArg() == 0 {
		return nil
	}
	return fmt.Errorf("%s does not accept positional arguments: %s", command, strings.Join(fs.Args(), ", "))
}

func runMQTTPublish(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("mqtt publish", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	payloadFile := fs.String("payload-file", "", "payload JSON file")
	brokerURL := fs.String("broker-url", "", "MQTT broker URL")
	topic := fs.String("topic", "", "MQTT topic")
	clientID := fs.String("client-id", "spex-probe", "MQTT client ID")
	qos := fs.Int("qos", 1, "MQTT QoS")
	timeoutValue := fs.String("timeout", "30s", "timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "mqtt publish"); err != nil {
		return err
	}
	if *payloadFile == "" || *brokerURL == "" || *topic == "" {
		return fmt.Errorf("mqtt publish requires --broker-url, --topic, and --payload-file")
	}
	payload, err := os.ReadFile(*payloadFile)
	if err != nil {
		return err
	}
	timeout, err := time.ParseDuration(*timeoutValue)
	if err != nil {
		return fmt.Errorf("invalid --timeout: %w", err)
	}
	if timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	if *qos < 0 || *qos > 2 {
		return fmt.Errorf("--qos must be 0, 1, or 2")
	}
	username, password := mqttCredentialsFromEnv()
	if err := publishMQTT(mqttPublishRequest{
		BrokerURL: *brokerURL,
		Topic:     *topic,
		ClientID:  *clientID,
		Username:  username,
		Password:  password,
		Payload:   payload,
		Timeout:   timeout,
		QoS:       byte(*qos),
	}); err != nil {
		return emitFailure(stdout, "mqtt.publish", err)
	}
	return emit(stdout, "mqtt.publish", "passed", "")
}

func runRedpandaSnapshotOffsets(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("redpanda snapshot-offsets", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	offsetsConfigMap := fs.String("offsets-configmap", "", "runtime offsets ConfigMap name")
	offsetsFile := fs.String("offsets-file", "", "local offsets file")
	brokers := fs.String("brokers", "", "comma-separated brokers")
	namespace := fs.String("namespace", "", "Kubernetes namespace")
	scenario := fs.String("scenario", "", "scenario label value")
	runID := fs.String("run-id", "", "run ID label value")
	timeoutValue := fs.String("timeout", "30s", "timeout")
	var topics stringList
	fs.Var(&topics, "topic", "topic to snapshot")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "redpanda snapshot-offsets"); err != nil {
		return err
	}
	if *offsetsConfigMap == "" && *offsetsFile == "" {
		return fmt.Errorf("redpanda snapshot-offsets requires --offsets-configmap or --offsets-file")
	}
	if *brokers == "" || len(topics) == 0 {
		return fmt.Errorf("redpanda snapshot-offsets requires --brokers and at least one --topic")
	}
	timeout, err := time.ParseDuration(*timeoutValue)
	if err != nil {
		return fmt.Errorf("invalid --timeout: %w", err)
	}
	if timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	offsets, err := snapshotRedpandaOffsets(*brokers, []string(topics), timeout)
	if err != nil {
		return emitFailure(stdout, "redpanda.snapshotOffsets", err)
	}
	store := redpandaOffsetStore(*offsetsConfigMap, *offsetsFile, *namespace, *scenario, *runID)
	if err := store.Save(redpandaOffsetSnapshot{
		APIVersion:    "spex.offsets.v0.1",
		ScenarioRunID: *runID,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		Topics:        offsets,
	}); err != nil {
		return emitFailure(stdout, "redpanda.snapshotOffsets", err)
	}
	return emit(stdout, "redpanda.snapshotOffsets", "passed", "")
}

func runGraphQLExpect(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("graphql expect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	queryFile := fs.String("query-file", "", "GraphQL query file")
	variablesFile := fs.String("variables-file", "", "GraphQL variables JSON file")
	matchersFile := fs.String("matchers-file", "", "matchers JSON file")
	fixtureResponseFile := fs.String("fixture-response-file", "", "fixture response JSON file")
	endpoint := fs.String("endpoint", "", "GraphQL endpoint")
	keycloakTokenURL := fs.String("keycloak-token-url", "", "Keycloak token endpoint URL")
	keycloakClientID := fs.String("keycloak-client-id", "", "Keycloak client ID")
	var keycloakScopes stringList
	fs.Var(&keycloakScopes, "keycloak-scope", "Keycloak OAuth scope")
	timeoutValue := fs.String("timeout", "30s", "timeout")
	pollIntervalValue := fs.String("poll-interval", "1s", "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "graphql expect"); err != nil {
		return err
	}
	for _, path := range []string{*queryFile, *variablesFile, *matchersFile} {
		if path == "" {
			return fmt.Errorf("graphql expect requires --query-file, --variables-file, and --matchers-file")
		}
		if _, err := os.Stat(path); err != nil {
			return err
		}
	}
	if *fixtureResponseFile != "" {
		if err := EvaluateGraphQLMatchersFile(*matchersFile, *fixtureResponseFile); err != nil {
			return emitFailure(stdout, "graphql.expect", err)
		}
		return emit(stdout, "graphql.expect", "passed", "")
	}
	if *endpoint == "" {
		return fmt.Errorf("graphql expect requires --endpoint unless --fixture-response-file is used")
	}
	timeout, err := time.ParseDuration(*timeoutValue)
	if err != nil {
		return fmt.Errorf("invalid --timeout: %w", err)
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
	auth := graphQLAuth{
		KeycloakTokenURL: *keycloakTokenURL,
		KeycloakClientID: *keycloakClientID,
		KeycloakScopes:   []string(keycloakScopes),
	}
	if err := expectGraphQL(*endpoint, *queryFile, *variablesFile, *matchersFile, timeout, pollInterval, auth); err != nil {
		return emitFailure(stdout, "graphql.expect", err)
	}
	return emit(stdout, "graphql.expect", "passed", "")
}

func runRedpandaContains(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("redpanda contains", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	matchersFile := fs.String("matchers-file", "", "matchers JSON file")
	offsetsConfigMap := fs.String("offsets-configmap", "", "runtime offsets ConfigMap name")
	offsetsFile := fs.String("offsets-file", "", "local offsets file")
	fixtureEventFile := fs.String("fixture-event-file", "", "fixture event JSON file")
	brokers := fs.String("brokers", "", "comma-separated brokers")
	topic := fs.String("topic", "", "topic")
	namespace := fs.String("namespace", "", "Kubernetes namespace")
	scenario := fs.String("scenario", "", "scenario label value")
	runID := fs.String("run-id", "", "run ID label value")
	timeoutValue := fs.String("timeout", "30s", "timeout")
	pollIntervalValue := fs.String("poll-interval", "1s", "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "redpanda contains"); err != nil {
		return err
	}
	if (*offsetsConfigMap == "" && *offsetsFile == "") || *matchersFile == "" {
		return fmt.Errorf("redpanda contains requires --offsets-configmap or --offsets-file, and --matchers-file")
	}
	if _, err := os.Stat(*matchersFile); err != nil {
		return err
	}
	if *fixtureEventFile != "" {
		if err := EvaluateMatchersFile(*matchersFile, *fixtureEventFile); err != nil {
			return emitFailure(stdout, "redpanda.contains", err)
		}
		return emit(stdout, "redpanda.contains", "passed", "")
	}
	if *brokers == "" || *topic == "" {
		return fmt.Errorf("redpanda contains requires --brokers and --topic unless --fixture-event-file is used")
	}
	timeout, err := time.ParseDuration(*timeoutValue)
	if err != nil {
		return fmt.Errorf("invalid --timeout: %w", err)
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
	store := redpandaOffsetStore(*offsetsConfigMap, *offsetsFile, *namespace, *scenario, *runID)
	snapshot, err := store.Load()
	if err != nil {
		return emitFailure(stdout, "redpanda.contains", err)
	}
	if err := redpandaContains(*brokers, *topic, snapshot.Topics[*topic], *matchersFile, timeout, pollInterval); err != nil {
		return emitFailure(stdout, "redpanda.contains", err)
	}
	return emit(stdout, "redpanda.contains", "passed", "")
}

type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func emit(stdout io.Writer, operation, status, reason string) error {
	result := map[string]string{
		"apiVersion": "spex.probe.result.v0.1",
		"operation":  operation,
		"status":     status,
	}
	if reason != "" {
		result["reason"] = reason
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(encoded))
	return nil
}

func emitFailure(stdout io.Writer, operation string, err error) error {
	if err == nil {
		err = fmt.Errorf("probe failed")
	}
	result := map[string]string{
		"apiVersion":   "spex.probe.result.v0.1",
		"operation":    operation,
		"status":       "failed",
		"failureClass": probeFailureClass(operation, err),
		"reason":       err.Error(),
	}
	encoded, emitErr := json.Marshal(result)
	if emitErr != nil {
		return emitErr
	}
	fmt.Fprintln(stdout, string(encoded))
	return err
}

func probeFailureClass(operation string, err error) string {
	message := ""
	if err != nil {
		message = strings.ToLower(err.Error())
	}
	switch operation {
	case "mqtt.publish":
		return "mqtt_publish_failed"
	case "mongodb.expect":
		if strings.Contains(message, "mongodb expectation timed out") || strings.Contains(message, "timed out") || strings.Contains(message, "timeout") || strings.Contains(message, "context deadline exceeded") {
			return "mongodb_match_timeout"
		}
		return "mongodb_expect_failed"
	case "postgresql.expect":
		if strings.Contains(message, "postgresql expectation timed out") || strings.Contains(message, "timed out") || strings.Contains(message, "timeout") || strings.Contains(message, "context deadline exceeded") {
			return "postgresql_match_timeout"
		}
		return "postgresql_expect_failed"
	case "rabbitmq.publish":
		return "rabbitmq_publish_failed"
	case "rabbitmq.expect":
		if strings.Contains(message, "rabbitmq expectation timed out") || strings.Contains(message, "timed out") || strings.Contains(message, "timeout") || strings.Contains(message, "context deadline exceeded") {
			return "rabbitmq_match_timeout"
		}
		return "rabbitmq_expect_failed"
	case "redpanda.snapshotOffsets":
		return "redpanda_offset_snapshot_failed"
	case "redpanda.contains":
		if strings.Contains(message, "redpanda_partition_set_changed") {
			return "redpanda_partition_set_changed"
		}
		if strings.Contains(message, "not found before timeout") || strings.Contains(message, "timed out") || strings.Contains(message, "timeout") {
			return "redpanda_match_timeout"
		}
	case "graphql.expect":
		if strings.Contains(message, "graphql response contains errors") {
			return "graphql_response_error"
		}
		if strings.Contains(message, "graphql expectation timed out") || strings.Contains(message, "timed out") || strings.Contains(message, "timeout") || strings.Contains(message, "context deadline exceeded") {
			return "graphql_match_timeout"
		}
	}
	return "probe_job_failed"
}
