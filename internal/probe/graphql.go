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
	"strings"
	"time"
)

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

var graphQLHTTPClient = http.DefaultClient

type graphQLAuth struct {
	KeycloakTokenURL string
	KeycloakClientID string
	KeycloakScopes   []string
}

func expectGraphQL(endpoint, queryFile, variablesFile, matchersFile string, timeout, pollInterval time.Duration, auth graphQLAuth) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	query, err := os.ReadFile(queryFile)
	if err != nil {
		return err
	}
	variables, err := loadVariables(variablesFile)
	if err != nil {
		return err
	}
	token, err := graphQLBearerToken(ctx, auth)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
poll:
	for {
		document, err := postGraphQL(ctx, endpoint, string(query), variables, token)
		if err == nil {
			if err := EvaluateGraphQLMatchersFileAgainstDocument(matchersFile, document); err == nil {
				return nil
			} else {
				lastErr = err
			}
		} else {
			lastErr = err
		}
		if time.Now().Add(pollInterval).After(deadline) {
			break
		}
		select {
		case <-time.After(pollInterval):
		case <-ctx.Done():
			if lastErr == nil {
				lastErr = ctx.Err()
			}
			break poll
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("graphql expectation timed out")
	}
	return fmt.Errorf("graphql expectation timed out: %w", lastErr)
}

func loadVariables(path string) (map[string]any, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var variables map[string]any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&variables); err != nil {
		return nil, fmt.Errorf("variables: %w", err)
	}
	return variables, nil
}

func graphQLBearerToken(ctx context.Context, auth graphQLAuth) (string, error) {
	if auth.KeycloakTokenURL == "" {
		return os.Getenv("SPEX_GRAPHQL_TOKEN"), nil
	}
	if auth.KeycloakClientID == "" {
		return "", fmt.Errorf("--keycloak-client-id is required when --keycloak-token-url is set")
	}
	clientSecret := os.Getenv("SPEX_GRAPHQL_KEYCLOAK_CLIENT_SECRET")
	if clientSecret == "" {
		return "", fmt.Errorf("SPEX_GRAPHQL_KEYCLOAK_CLIENT_SECRET is required when --keycloak-token-url is set")
	}
	return fetchKeycloakToken(ctx, auth.KeycloakTokenURL, auth.KeycloakClientID, clientSecret, auth.KeycloakScopes)
}

func fetchKeycloakToken(ctx context.Context, tokenURL, clientID, clientSecret string, scopes []string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	if len(scopes) > 0 {
		form.Set("scope", strings.Join(scopes, " "))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := graphQLHTTPClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("keycloak token endpoint returned HTTP %d: %s", response.StatusCode, string(body))
	}
	var decoded struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("keycloak token response: %w", err)
	}
	if decoded.AccessToken == "" {
		return "", fmt.Errorf("keycloak token response missing access_token")
	}
	return decoded.AccessToken, nil
}

func postGraphQL(ctx context.Context, endpoint, query string, variables map[string]any, token string) (any, error) {
	body, err := json.Marshal(graphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := graphQLHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("graphql endpoint returned HTTP %d: %s", response.StatusCode, string(responseBody))
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("graphql response: %w", err)
	}
	if err := rejectGraphQLErrors(document); err != nil {
		return nil, err
	}
	return document, nil
}

func EvaluateGraphQLMatchersFile(matchersPath, documentPath string) error {
	matchers, err := loadMatchers(matchersPath)
	if err != nil {
		return err
	}
	document, err := loadJSON(documentPath)
	if err != nil {
		return err
	}
	if err := rejectGraphQLErrors(document); err != nil {
		return err
	}
	return EvaluateMatchers(matchers, document)
}

func EvaluateGraphQLMatchersFileAgainstDocument(matchersPath string, document any) error {
	if err := rejectGraphQLErrors(document); err != nil {
		return err
	}
	return EvaluateMatchersFileAgainstDocument(matchersPath, document)
}

func rejectGraphQLErrors(document any) error {
	object, ok := document.(map[string]any)
	if !ok {
		return nil
	}
	errorsValue, ok := object["errors"]
	if !ok || errorsValue == nil {
		return nil
	}
	errorsArray, ok := errorsValue.([]any)
	if ok && len(errorsArray) == 0 {
		return nil
	}
	encoded, err := json.Marshal(errorsValue)
	if err != nil {
		return fmt.Errorf("graphql response contains errors")
	}
	return fmt.Errorf("graphql response contains errors: %s", string(encoded))
}
