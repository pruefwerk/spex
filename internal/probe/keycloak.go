package probe

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func runKeycloakOperation(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("keycloak run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	operationFile := fs.String("operation-file", "", "lowered operation JSON file")
	resultFile := fs.String("result-file", "", "normalized result envelope path")
	timeoutValue := fs.String("timeout", "", "timeout override")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectProbePositionalArgs(fs, "keycloak run"); err != nil {
		return err
	}
	if *operationFile == "" || *resultFile == "" {
		return fmt.Errorf("keycloak run requires --operation-file and --result-file")
	}
	operation, err := readLoweredOperation(*operationFile)
	if err != nil {
		return err
	}
	if operation.OperationType != "keycloak.token" || operation.Provider != "keycloak" {
		return fmt.Errorf("keycloak run cannot execute operation type %q from provider %q", operation.OperationType, operation.Provider)
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
	result, err := executeKeycloakLoweredOperation(operation, timeout)
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

func executeKeycloakLoweredOperation(operation probeLoweredOperation, timeout time.Duration) (map[string]any, error) {
	tokenURL, _ := operation.Binding.With["tokenURL"].(string)
	clientID, _ := operation.Binding.With["clientID"].(string)
	scopes := stringSliceFromAny(operation.Binding.With["scopes"])
	clientSecret := os.Getenv("SPEX_KEYCLOAK_CLIENT_SECRET")
	if clientSecret == "" {
		return nil, fmt.Errorf("SPEX_KEYCLOAK_CLIENT_SECRET is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	token, err := fetchKeycloakTokenResponse(ctx, tokenURL, clientID, clientSecret, scopes)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"tokenType":      token.TokenType,
		"expiresIn":      token.ExpiresIn,
		"scope":          token.Scope,
		"hasAccessToken": token.AccessToken != "",
	}
	if claims, ok := decodeJWTClaims(token.AccessToken); ok {
		result["claims"] = claims
	}
	if matchers, ok := operation.With["match"]; ok && matchers != nil {
		if err := evaluateMatchersAgainstValue(matchers, result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func decodeJWTClaims(token string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil {
		return nil, false
	}
	return claims, true
}

func stringSliceFromAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, fmt.Sprint(item))
		}
		return out
	default:
		return nil
	}
}

func evaluateMatchersAgainstValue(matchers any, value any) error {
	content, err := json.Marshal(matchers)
	if err != nil {
		return err
	}
	var parsed []Matcher
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return fmt.Errorf("matchers: %w", err)
	}
	return EvaluateMatchers(parsed, value)
}
