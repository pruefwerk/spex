package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/pruefwerk/spex/pkg/runtimehelpers"
)

// This command is a private before-scenario hook, never a normal CI shell step.
func (h *Host) renewCredentials(ctx context.Context, out io.Writer) error {
	env := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	policy := h.config.Credentials
	if !regexp.MustCompile(`^[0-9]{12}$`).MatchString(env[policy.AccountEnvironment]) {
		return errors.New("AWS scenario credential renewal failed")
	}
	values, err := runtimehelpers.RenewAWSCredentials(ctx, env, runtimehelpers.AWSCredentialPolicy{RoleARN: "arn:aws:iam::" + env[policy.AccountEnvironment] + ":role/" + policy.Role, SessionName: policy.Session, Region: env[policy.RegionEnvironment], DurationSeconds: policy.DurationSeconds}, nil, nil)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(values)
}
