package runtimehelpers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type AWSCredentialPolicy struct {
	RoleARN, SessionName, Region string
	DurationSeconds              int
}
type CredentialCommand func(context.Context, []string, []string) ([]byte, error)

// RenewAWSCredentials returns credentials only to its caller's private pipe.
// Runtime hosts own the role, region and session policy. Errors contain no raw
// HTTP, subprocess or credential-bearing response data.
func RenewAWSCredentials(ctx context.Context, env map[string]string, policy AWSCredentialPolicy, client *http.Client, run CredentialCommand) (map[string]string, error) {
	fail := errors.New("AWS scenario credential renewal failed")
	endpoint, err := url.Parse(env["ACTIONS_ID_TOKEN_REQUEST_URL"])
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || env["ACTIONS_ID_TOKEN_REQUEST_TOKEN"] == "" || !regexp.MustCompile(`^arn:aws:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]+$`).MatchString(policy.RoleARN) || !regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{2,64}$`).MatchString(policy.SessionName) || !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(policy.Region) || policy.DurationSeconds < 900 || policy.DurationSeconds > 43200 {
		return nil, fail
	}
	query := endpoint.Query()
	query.Set("audience", "sts.amazonaws.com")
	endpoint.RawQuery = query.Encode()
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fail
	}
	request.Header.Set("Authorization", "Bearer "+env["ACTIONS_ID_TOKEN_REQUEST_TOKEN"])
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	privateClient := *client
	privateClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := privateClient.Do(request)
	if err != nil {
		return nil, fail
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 || response.StatusCode != http.StatusOK {
		return nil, fail
	}
	var token struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(data, &token) != nil || token.Value == "" {
		return nil, fail
	}
	dir, err := os.MkdirTemp("", "spex-oidc-")
	if err != nil {
		return nil, fail
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte(token.Value), 0600); err != nil {
		return nil, fail
	}
	clean := []string{}
	for key, value := range env {
		switch key {
		case "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_SECURITY_TOKEN", "AWS_PAGER":
			continue
		}
		clean = append(clean, key+"="+value)
	}
	clean = append(clean, "AWS_PAGER=")
	if run == nil {
		run = func(ctx context.Context, args, environment []string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, args[0], args[1:]...)
			cmd.Env = environment
			prepareProcess(cmd)
			cmd.WaitDelay = 5 * time.Second
			var out limitedBuffer
			cmd.Stdout = &out
			cmd.Stderr = io.Discard
			err := cmd.Run()
			if out.overflow {
				return nil, fail
			}
			return out.Bytes(), err
		}
	}
	stsCtx, stop := context.WithTimeout(ctx, 40*time.Second)
	defer stop()
	data, err = run(stsCtx, []string{"aws", "sts", "assume-role-with-web-identity", "--role-arn", policy.RoleARN, "--role-session-name", policy.SessionName, "--web-identity-token", "file://" + path, "--duration-seconds", strconv.Itoa(policy.DurationSeconds), "--region", policy.Region, "--output", "json"}, clean)
	if err != nil {
		return nil, fail
	}
	var result struct {
		Credentials struct {
			AccessKeyID string `json:"AccessKeyId"`
			Secret      string `json:"SecretAccessKey"`
			Token       string `json:"SessionToken"`
			Expiration  string
		}
	}
	if json.Unmarshal(data, &result) != nil {
		return nil, fail
	}
	expires, err := time.Parse(time.RFC3339, result.Credentials.Expiration)
	if err != nil || !expires.After(time.Now()) || result.Credentials.AccessKeyID == "" || result.Credentials.Secret == "" || result.Credentials.Token == "" {
		return nil, fail
	}
	values := map[string]string{"AWS_ACCESS_KEY_ID": result.Credentials.AccessKeyID, "AWS_SECRET_ACCESS_KEY": result.Credentials.Secret, "AWS_SESSION_TOKEN": result.Credentials.Token, "AWS_REGION": policy.Region, "AWS_DEFAULT_REGION": policy.Region}
	for _, value := range values {
		if strings.ContainsAny(value, "\r\n") {
			return nil, fail
		}
	}
	return values, nil
}
