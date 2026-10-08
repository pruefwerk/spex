package runtimehelpers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type credentialTransport func(*http.Request) (*http.Response, error)

func (f credentialTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeCredentialRenewalKeepsTokensPrivate(t *testing.T) {
	env := map[string]string{"ACTIONS_ID_TOKEN_REQUEST_URL": "https://example.invalid/token?api-version=2&audience=old", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "SENTINEL_REQUEST_TOKEN", "AWS_ACCESS_KEY_ID": "SENTINEL_OLD_ACCESS", "AWS_SECRET_ACCESS_KEY": "SENTINEL_OLD_SECRET", "AWS_SESSION_TOKEN": "SENTINEL_OLD_SESSION"}
	policy := AWSCredentialPolicy{"arn:aws:iam::123456789012:role/test", "Test-Session", "eu-central-1", 3600}
	client := &http.Client{Transport: credentialTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("audience") != "sts.amazonaws.com" || len(r.URL.Query()["audience"]) != 1 || r.Header.Get("Authorization") != "Bearer SENTINEL_REQUEST_TOKEN" {
			t.Fatal("OIDC request changed")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"value":"SENTINEL_OIDC_TOKEN"}`))}, nil
	})}
	var tokenPath string
	run := func(ctx context.Context, args, environment []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "SENTINEL_OIDC_TOKEN") || strings.Contains(strings.Join(environment, " "), "SENTINEL_OLD_") {
			t.Fatal("token in argv or stale credentials retained")
		}
		for i, arg := range args {
			if arg == "--web-identity-token" {
				tokenPath = strings.TrimPrefix(args[i+1], "file://")
			}
		}
		data, err := os.ReadFile(tokenPath)
		info, statErr := os.Stat(tokenPath)
		if err != nil || statErr != nil || string(data) != "SENTINEL_OIDC_TOKEN" || info.Mode().Perm() != 0600 {
			t.Fatal("token file not private")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 40*time.Second {
			t.Fatal("STS request unbounded")
		}
		return json.Marshal(map[string]any{"Credentials": map[string]string{"AccessKeyId": "fresh-access", "SecretAccessKey": "SENTINEL_FRESH_SECRET", "SessionToken": "SENTINEL_FRESH_SESSION", "Expiration": time.Now().Add(time.Hour).Format(time.RFC3339)}})
	}
	values, err := RenewAWSCredentials(context.Background(), env, policy, client, run)
	if err != nil || values["AWS_ACCESS_KEY_ID"] != "fresh-access" {
		t.Fatal("renewal failed", err)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatal("token retained")
	}
	for _, response := range []string{`{"Credentials":{"Expiration":"2000-01-01T00:00:00Z"}}`, `{"Credentials":{"AccessKeyId":"a","SecretAccessKey":"s","SessionToken":"t","Expiration":"2099-01-01T00:00:00"}}`, `{"Credentials":{"AccessKeyId":"a","SecretAccessKey":"s","Expiration":"2099-01-01T00:00:00Z"}}`} {
		if _, err := RenewAWSCredentials(context.Background(), env, policy, client, func(context.Context, []string, []string) ([]byte, error) { return []byte(response), nil }); err == nil {
			t.Fatal("invalid credentials accepted")
		}
	}
	_, err = RenewAWSCredentials(context.Background(), env, policy, client, func(context.Context, []string, []string) ([]byte, error) {
		return nil, errors.New("SENTINEL_RAW_ERROR")
	})
	if err == nil || strings.Contains(err.Error(), "SENTINEL") {
		t.Fatal("raw credential failure exposed")
	}
}

func TestNativeCredentialRenewalRejectsMissingContextAndRedirects(t *testing.T) {
	policy := AWSCredentialPolicy{"arn:aws:iam::123456789012:role/test", "Test-Session", "eu-central-1", 3600}
	calls := 0
	client := &http.Client{Transport: credentialTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://foreign.invalid/token"}}, Body: io.NopCloser(strings.NewReader("SENTINEL_BODY"))}, nil
	})}
	for _, endpoint := range []string{"", "http://foreign.invalid", "https://user:password@foreign.invalid"} {
		if _, err := RenewAWSCredentials(context.Background(), map[string]string{"ACTIONS_ID_TOKEN_REQUEST_URL": endpoint, "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "SENTINEL"}, policy, client, nil); err == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid context reached network")
	}
	_, err := RenewAWSCredentials(context.Background(), map[string]string{"ACTIONS_ID_TOKEN_REQUEST_URL": "https://example.invalid", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "SENTINEL"}, policy, client, nil)
	if err == nil || calls != 1 || strings.Contains(err.Error(), "SENTINEL") {
		t.Fatal("redirect followed or leaked")
	}
}
