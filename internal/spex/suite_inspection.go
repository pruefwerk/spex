package spex

import (
	"context"
	"io"
	"strings"

	"github.com/pruefwerk/spex/internal/workspace"
)

// SuiteTest describes discovery without compiling jobs or preparing an environment.
type SuiteTest struct {
	Name string   `json:"name"`
	File string   `json:"file"`
	Tags []string `json:"tags,omitempty"`
}

func DiscoverSuite(ctx context.Context, path string) ([]SuiteTest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, err := workspace.LoadScenarioSuite(path)
	if err != nil {
		return nil, err
	}
	inputs, err := loadSuiteInputs(resolved, suiteFlags{suitePath: path})
	if err != nil {
		return nil, err
	}
	tests := make([]SuiteTest, 0, len(inputs))
	for _, input := range inputs {
		tests = append(tests, SuiteTest{input.ScenarioName, input.ScenarioPath, append([]string(nil), input.Scenario.Metadata.Tags...)})
	}
	return tests, ctx.Err()
}

func ValidateSuite(ctx context.Context, path string, tags []string, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	args := []string{"--suite", path}
	if len(tags) > 0 {
		args = append(args, "--include-any-tag", strings.Join(tags, ","))
	}
	if err := runSuiteValidate(args, out); err != nil {
		return err
	}
	return ctx.Err()
}

func GroupSummary(root, title string, out io.Writer) error {
	return runGroupSummary([]string{"--root", root, "--title", title}, out)
}
