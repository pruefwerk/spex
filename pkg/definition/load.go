// Package definition handles Spex authoring sources independently of a runtime.
package definition

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pruefwerk/spex/pkg/scenario"
	"gopkg.in/yaml.v3"
)

// definitionKind identifies the document envelope, not its runtime semantics.
// Never reinterpret a malformed scenario envelope as a test definition.
func definitionKind(content string) (string, error) {
	var envelope map[string]any
	if err := toml.Unmarshal([]byte(content), &envelope); err == nil {
		if _, present := envelope["schema"]; present {
			return "toml", nil
		}
	}
	if Extension(content) == ".feature" {
		return "gherkin", nil
	}
	var header struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}
	if err := yaml.Unmarshal([]byte(content), &header); err == nil && header.APIVersion == "spex.scenario.v0.1" && header.Kind == "Scenario" {
		return "yaml", nil
	}
	return "", errors.New("definition must be scenario TOML, Spex YAML or Gherkin")
}

func Load(inline *string, files []string, root string, read func(string) ([]byte, error), request bool) (*scenario.Scenario, []scenario.TestSource, error) {
	if inline != nil && len(files) > 0 {
		return nil, nil, errors.New("choose definition or definition-files, not both")
	}
	var err error
	files, err = ExpandFiles(root, files)
	if err != nil {
		return nil, nil, err
	}
	parse := scenario.Parse
	if request {
		parse = scenario.ParseRequest
	}
	var document *scenario.Scenario
	var tests []scenario.TestSource
	consume := func(content, file string, ordinal int) error {
		kind, err := definitionKind(content)
		if err != nil {
			return fmt.Errorf("definition %d: %w", ordinal, err)
		}
		if kind == "toml" {
			if document != nil || len(tests) > 0 {
				return errors.New("one TOML definition cannot be combined with other definitions")
			}
			parsed, err := parse([]byte(content))
			if err != nil {
				return fmt.Errorf("definition %d: %w", ordinal, err)
			}
			document = &parsed
			return nil
		}
		if document != nil {
			return errors.New("one TOML definition cannot be combined with other definitions")
		}
		if file != "" {
			// The existing external-source loader uses this extension for Gherkin.
			if (kind == "gherkin") != strings.HasSuffix(file, ".feature") {
				return errors.New("Gherkin definition files must use .feature; YAML files must not")
			}
			tests = append(tests, scenario.TestSource{File: &file})
		} else {
			tests = append(tests, scenario.TestSource{Inline: &content})
		}
		return nil
	}
	if inline != nil {
		text := *inline
		// Parse complete TOML first: its inline strings may themselves contain ---.
		if kind, _ := definitionKind(text); kind == "toml" {
			if err := consume(text, "", 1); err != nil {
				return nil, nil, err
			}
		} else {
			parts, err := SplitInline(text)
			if err != nil {
				return nil, nil, err
			}
			if len(parts) > 1 {
				for _, part := range parts {
					if kind, _ := definitionKind(*part.Inline); kind == "toml" {
						return nil, nil, errors.New("TOML must be a single complete definition")
					}
				}
			}
			for i, part := range parts {
				if err := consume(*part.Inline, "", i+1); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	seen := map[string]bool{}
	for i, file := range files {
		path, err := scenario.SourcePath(root, file)
		if err != nil {
			return nil, nil, errors.New("definition file unavailable or outside workspace")
		}
		if seen[path] {
			return nil, nil, errors.New("duplicate definition file")
		}
		seen[path] = true
		data, err := read(path)
		if err != nil {
			return nil, nil, err
		}
		if kind, _ := definitionKind(string(data)); kind == "toml" && len(files) != 1 {
			return nil, nil, errors.New("TOML must be the only definition file")
		}
		if kind, _ := definitionKind(string(data)); kind != "toml" {
			parts, err := SplitInline(string(data))
			if err != nil || len(parts) != 1 {
				return nil, nil, errors.New("each external test file must contain one document; use separate files or inline definition")
			}
		}
		if err := consume(string(data), file, i+1); err != nil {
			return nil, nil, err
		}
	}
	return document, tests, nil
}
