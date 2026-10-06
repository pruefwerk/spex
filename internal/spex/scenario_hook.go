package spex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var hookEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Renew runtime inputs just before execution, not when compiling the suite.
// Each scenario receives its own environment; concurrent runs never mutate the
// parent process or share a credential file.
func runSuiteScenario(path string, flags suiteFlags, stdout, stderr io.Writer) error {
	ctx := suiteContext(flags)
	if err := ctx.Err(); err != nil {
		return err
	}
	var environment []string
	if flags.beforeScenarioHook != "" {
		var err error
		environment, err = executeScenarioHookContext(ctx, path, flags.beforeScenarioHook, flags.beforeScenarioHookTimeout)
		if err != nil {
			class := "before_scenario_hook_failed"
			message := "Before-scenario hook failed; scenario did not run"
			now := time.Now().UTC()
			_, reportErr := WriteReport(ReportInput{Workspace: path, StartedAt: now, FinishedAt: now,
				ScenarioResult: "not_run", RunnerResult: "error", FailureClass: &class, FailureMessage: &message})
			if reportErr != nil {
				return fmt.Errorf("before-scenario hook failed; failure report could not be written")
			}
			return fmt.Errorf("%s", message)
		}
	}
	return runWorkspaceContext(ctx, suiteWorkspaceRunArgs(path, flags), stdout, stderr, environment)
}

func suiteContext(flags suiteFlags) context.Context {
	if flags.ctx != nil {
		return flags.ctx
	}
	return context.Background()
}

func executeScenarioHook(path, executable string, timeout time.Duration) ([]string, error) {
	return executeScenarioHookContext(context.Background(), path, executable, timeout)
}

func executeScenarioHookContext(parent context.Context, path, executable string, timeout time.Duration) ([]string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("invalid hook workspace")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable)
	cancelCommandTree(cmd)
	cmd.Env = mergeScenarioEnvironment([]string{"SPEX_SCENARIO_WORKSPACE=" + absolute})
	cmd.WaitDelay = 2 * time.Second
	output := newLimitedCapture(64 << 10)
	cmd.Stdout = output
	cmd.Stderr = io.Discard // Hook output can contain credentials, including errors.
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("before-scenario hook failed or timed out")
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(output.String()), &values); err != nil || values == nil {
		return nil, fmt.Errorf("before-scenario hook returned invalid environment")
	}
	names := make([]string, 0, len(values))
	for name, value := range values {
		if !hookEnvironmentName.MatchString(name) || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("before-scenario hook returned invalid environment")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		environment = append(environment, name+"="+values[name])
	}
	return environment, nil
}

func scenarioCommandEnvironment(environments [][]string) []string {
	if len(environments) == 0 || len(environments[0]) == 0 {
		return nil
	}
	return mergeScenarioEnvironment(environments[0])
}

func mergeScenarioEnvironment(overrides []string) []string {
	keys := map[string]bool{}
	for _, value := range overrides {
		key, _, _ := strings.Cut(value, "=")
		keys[key] = true
	}
	out := make([]string, 0, len(os.Environ())+len(overrides))
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !keys[key] {
			out = append(out, value)
		}
	}
	return append(out, overrides...)
}
