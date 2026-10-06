package spex

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

type scenarioFiles []string

func (f *scenarioFiles) String() string         { return "" }
func (f *scenarioFiles) Set(value string) error { *f = append(*f, value); return nil }

// GitHub adapters must call these authoring commands rather than interpreting
// TOML, merging configuration, or constructing shell commands themselves.
func runScenarioCommand(args []string, stdout io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runScenarioCommandContext(ctx, args, stdout)
}

func runScenarioCommandContext(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return ExitError{Code: ExitValidation, Err: errors.New("scenario requires build, validate, explain, or run")}
	}
	command := args[0]
	if command != "build" && command != "validate" && command != "explain" && command != "run" {
		return ExitError{Code: ExitValidation, Err: errors.New("unknown scenario command")}
	}
	fs := flag.NewFlagSet("scenario "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("workspace", ".", "repository root for all test paths")
	var source, runtimeID, name, description, timeout, inlineFile, configFile, output string
	var files scenarioFiles
	artifactDirectory := ".spex/runs"
	if command == "run" {
		fs.StringVar(&artifactDirectory, "artifact-directory", artifactDirectory, "workspace-relative artifact base")
	}
	if command == "build" {
		fs.StringVar(&source, "scenario", "", "committed scenario to overlay")
		fs.StringVar(&runtimeID, "runtime", "", "runtime identifier")
		fs.StringVar(&name, "name", "", "scenario name")
		fs.StringVar(&description, "description", "", "scenario description")
		fs.StringVar(&timeout, "timeout", "", "scenario deadline")
		fs.StringVar(&inlineFile, "inline-file", "", "file containing inline YAML or Gherkin")
		fs.StringVar(&configFile, "runtime-config-file", "", "file containing a runtime TOML fragment")
		fs.StringVar(&output, "out", ".spex/scenario.toml", "new canonical file, relative to workspace")
		fs.Var(&files, "spex-file", "external test source, repeat for multiple files")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return ExitError{Code: ExitValidation, Err: errors.New("invalid scenario command arguments")}
	}
	if (command == "build" && fs.NArg() != 0) || (command != "build" && fs.NArg() != 1) {
		return ExitError{Code: ExitValidation, Err: errors.New("scenario validate/explain require one file; build accepts flags only")}
	}
	workspaceRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	read := func(path string) ([]byte, error) {
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspaceRoot, path)
		}
		data, err := readRegularEvidenceFile(path, scenario.MaxDocumentBytes)
		if err != nil {
			return nil, errors.New("scenario input is unavailable, not a regular file, or exceeds the size limit")
		}
		return data, nil
	}
	if command != "build" {
		source = fs.Arg(0)
	}
	var document scenario.Scenario
	if source != "" {
		data, err := read(source)
		if err != nil {
			return ExitError{Code: ExitValidation, Err: err}
		}
		document, err = scenario.Parse(data)
		if err != nil {
			return ExitError{Code: ExitValidation, Err: err}
		}
	}
	suite := os.Getenv("SPEX_SUITE")
	if suite == "" {
		suite = "suite.yaml"
	}
	defaults, err := parseSuiteFlags("run", []string{"--suite", suite})
	if err != nil {
		return err
	}
	runtime := migrationRuntime{flags: defaults}
	registry := scenarioruntime.NewRegistry()
	if err := registry.Register(runtime); err != nil {
		return err
	}
	if command == "build" {
		overrides := scenario.AuthoringOverrides{}
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "runtime":
				overrides.Runtime = &runtimeID
			case "name":
				overrides.Name = &name
			case "description":
				overrides.Description = &description
			case "timeout":
				overrides.Timeout = &timeout
			}
		})
		if inlineFile != "" && len(files) > 0 {
			return ExitError{Code: ExitValidation, Err: errors.New("choose inline source or external files, not both")}
		}
		if inlineFile != "" {
			content, err := read(inlineFile)
			if err != nil {
				return ExitError{Code: ExitValidation, Err: err}
			}
			text := string(content)
			overrides.Tests = []scenario.TestSource{{Inline: &text}}
		}
		for _, file := range files {
			value := file
			overrides.Tests = append(overrides.Tests, scenario.TestSource{File: &value})
		}
		if configFile != "" {
			data, err := read(configFile)
			if err != nil {
				return ExitError{Code: ExitValidation, Err: err}
			}
			config, err := scenario.ParseRuntimeConfig(data)
			if err != nil {
				return ExitError{Code: ExitValidation, Err: err}
			}
			overrides.RuntimeConfig = &config
		}
		if source == "" {
			document, err = scenario.Build(runtimeID, overrides, runtime.MergeConfig)
		} else {
			document, err = scenario.MergeAuthoringOverrides(document, overrides, true, runtime.MergeConfig)
		}
		if err != nil {
			return ExitError{Code: ExitValidation, Err: err}
		}
	}
	prepared, err := registry.Prepare(ctx, scenarioruntime.ResolveRequest{Scenario: document, Workspace: workspaceRoot})
	if err != nil {
		if ctx.Err() != nil {
			return ExitError{Code: 130, Err: errors.New("scenario cancelled before execution")}
		}
		var sourceError *scenarioSourceError
		if errors.As(err, &sourceError) {
			err = sourceError
		}
		return ExitError{Code: ExitValidation, Err: err}
	}
	id, err := scenario.Identity(prepared.Scenario)
	if err != nil {
		return err
	}
	if command == "build" {
		data, err := scenario.SerializeCanonical(prepared.Scenario)
		if err != nil {
			return err
		}
		if err := writeCanonicalScenario(workspaceRoot, output, data); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Scenario ID: %s\nScenario path: %s\n", id, output)
		return nil
	}
	if command == "validate" {
		fmt.Fprintf(stdout, "scenario valid: %s\n", id)
		return nil
	}
	if command == "run" {
		sink, err := scenarioruntime.NewFileArtifacts(workspaceRoot, artifactDirectory, id)
		if err != nil {
			return ExitError{Code: ExitPreflight, Err: err}
		}
		defer sink.Close()
		result, runErr := scenarioruntime.Run(ctx, prepared, sink)
		if err := json.NewEncoder(stdout).Encode(struct {
			ScenarioID        string                  `json:"scenario_id"`
			ScenarioPath      string                  `json:"scenario_path"`
			ResultPath        string                  `json:"result_path"`
			ArtifactDirectory string                  `json:"artifact_directory"`
			Runtime           string                  `json:"runtime"`
			Outcome           scenarioruntime.Outcome `json:"outcome"`
		}{id, filepath.Join(sink.Directory(), "scenario.toml"), filepath.Join(sink.Directory(), "result.json"), sink.Directory(), document.Runtime, result.Outcome}); err != nil {
			if runErr == nil {
				runErr = errors.New("could not write run summary")
			}
		}
		if runErr == nil {
			return nil
		}
		code := ExitPreflight
		if result.Outcome == scenarioruntime.Failed {
			code = ExitRuntime
		}
		if result.Outcome == scenarioruntime.Cancelled {
			code = 130
		}
		return ExitError{Code: code, Err: runErr}
	}
	return json.NewEncoder(stdout).Encode(struct {
		ID      string                      `json:"scenario_id"`
		Schema  string                      `json:"schema"`
		Runtime string                      `json:"runtime"`
		Name    string                      `json:"name,omitempty"`
		Plan    scenarioruntime.PlanSummary `json:"plan"`
	}{id, scenario.Schema, prepared.Scenario.Runtime, prepared.Scenario.Metadata.Name, prepared.Plan.Summary()})
}

func writeCanonicalScenario(workspaceRoot, name string, data []byte) error {
	name, err := scenario.RelativePath(name)
	if err != nil {
		return errors.New("canonical output must remain within workspace")
	}
	root, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		return errors.New("workspace unavailable")
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return errors.New("cannot create canonical output directory")
	}
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("canonical output already exists or is not writable")
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("cannot write canonical scenario")
	}
	return nil
}
