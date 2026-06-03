package probe

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

func runRedisOperation(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("redis run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	operationFile := fs.String("operation-file", "", "lowered operation JSON file")
	resultFile := fs.String("result-file", "", "normalized result envelope path")
	timeoutValue := fs.String("timeout", "", "timeout override")
	pollIntervalValue := fs.String("poll-interval", "1s", "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "redis run"); err != nil {
		return err
	}
	if *operationFile == "" || *resultFile == "" {
		return fmt.Errorf("redis run requires --operation-file and --result-file")
	}
	operation, err := readLoweredOperation(*operationFile)
	if err != nil {
		return err
	}
	if operation.Provider != "redis" {
		return fmt.Errorf("redis run cannot execute operation type %q from provider %q", operation.OperationType, operation.Provider)
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
	result, err := executeRedisLoweredOperation(operation, timeout, pollInterval)
	if result == nil {
		result = map[string]any{}
	}
	envelope := probeResultEnvelope{
		OperationID:   operation.OperationID,
		OperationType: operation.OperationType,
		Provider:      operation.Provider,
		Status:        "passed",
		Result:        result,
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

func executeRedisLoweredOperation(operation probeLoweredOperation, timeout, pollInterval time.Duration) (map[string]any, error) {
	key, _ := operation.With["key"].(string)
	equals, _ := operation.With["equals"].(string)
	uri, _ := operation.Binding.With["uri"].(string)
	client, err := newRedisClient(uri)
	if err != nil {
		return nil, err
	}
	defer client.close()
	if err := client.connect(timeout); err != nil {
		return nil, err
	}
	switch operation.OperationType {
	case "redis.get":
		value, ok, err := client.get(key)
		if err != nil {
			return nil, err
		}
		return map[string]any{"key": key, "exists": ok, "value": value}, nil
	case "redis.assertKeyExists":
		if err := redisEventually(timeout, pollInterval, func() error {
			ok, err := client.exists(key)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("redis key %q does not exist", key)
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return map[string]any{"key": key, "exists": true}, nil
	case "redis.assertValueEquals":
		if err := redisEventually(timeout, pollInterval, func() error {
			value, ok, err := client.get(key)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("redis key %q does not exist", key)
			}
			if value != equals {
				return fmt.Errorf("redis key %q value %q does not equal %q", key, value, equals)
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return map[string]any{"key": key, "value": equals}, nil
	default:
		return nil, fmt.Errorf("redis run cannot execute operation type %q", operation.OperationType)
	}
}

func redisEventually(timeout, pollInterval time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().Add(pollInterval).After(deadline) {
			return lastErr
		}
		time.Sleep(pollInterval)
	}
}

type redisClient struct {
	uri      string
	addr     string
	username string
	password string
	db       int
	conn     net.Conn
	reader   *bufio.Reader
}

func newRedisClient(uri string) (*redisClient, error) {
	if strings.TrimSpace(uri) == "" {
		return nil, fmt.Errorf("redis binding uri is required")
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("invalid redis uri: %w", err)
	}
	if parsed.Scheme != "redis" {
		return nil, fmt.Errorf("redis uri scheme must be redis")
	}
	addr := parsed.Host
	if !strings.Contains(addr, ":") {
		addr += ":6379"
	}
	db := 0
	if parsed.Path != "" && parsed.Path != "/" {
		value := strings.TrimPrefix(parsed.Path, "/")
		parsedDB, err := strconv.Atoi(value)
		if err != nil || parsedDB < 0 {
			return nil, fmt.Errorf("redis uri database must be a non-negative integer")
		}
		db = parsedDB
	}
	username := ""
	password := ""
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	envUsername, envPassword := redisCredentialsFromEnv()
	if envUsername != "" || envPassword != "" {
		username = envUsername
		password = envPassword
	}
	return &redisClient{uri: uri, addr: addr, username: username, password: password, db: db}, nil
}

func (c *redisClient) connect(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return err
	}
	c.conn = conn
	c.reader = bufio.NewReader(conn)
	if c.password != "" {
		args := []string{"AUTH", c.password}
		if c.username != "" {
			args = []string{"AUTH", c.username, c.password}
		}
		if _, err := c.command(args...); err != nil {
			return err
		}
	}
	if c.db > 0 {
		if _, err := c.command("SELECT", strconv.Itoa(c.db)); err != nil {
			return err
		}
	}
	return nil
}

func (c *redisClient) close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *redisClient) get(key string) (string, bool, error) {
	value, err := c.command("GET", key)
	if err != nil {
		if errors.Is(err, errRedisNil) {
			return "", false, nil
		}
		return "", false, err
	}
	text, ok := value.(string)
	if !ok {
		return "", false, fmt.Errorf("redis GET returned %T", value)
	}
	return text, true, nil
}

func (c *redisClient) exists(key string) (bool, error) {
	value, err := c.command("EXISTS", key)
	if err != nil {
		return false, err
	}
	count, ok := value.(int64)
	if !ok {
		return false, fmt.Errorf("redis EXISTS returned %T", value)
	}
	return count > 0, nil
}

func (c *redisClient) command(args ...string) (any, error) {
	if _, err := c.conn.Write(redisCommand(args)); err != nil {
		return nil, err
	}
	return readRedisValue(c.reader)
}

func redisCommand(args []string) []byte {
	var b strings.Builder
	b.WriteString("*")
	b.WriteString(strconv.Itoa(len(args)))
	b.WriteString("\r\n")
	for _, arg := range args {
		b.WriteString("$")
		b.WriteString(strconv.Itoa(len(arg)))
		b.WriteString("\r\n")
		b.WriteString(arg)
		b.WriteString("\r\n")
	}
	return []byte(b.String())
}

var errRedisNil = errors.New("redis nil")

func readRedisValue(r *bufio.Reader) (any, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	switch prefix {
	case '+':
		return line, nil
	case '-':
		return nil, errors.New(line)
	case ':':
		return strconv.ParseInt(line, 10, 64)
	case '$':
		length, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if length < 0 {
			return nil, errRedisNil
		}
		buf := make([]byte, length+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return string(buf[:length]), nil
	default:
		return nil, fmt.Errorf("unsupported redis response prefix %q", string(prefix))
	}
}

func redisCredentialsFromEnv() (string, string) {
	return os.Getenv("SPEX_REDIS_USERNAME"), os.Getenv("SPEX_REDIS_PASSWORD")
}
