package spex

import (
	"github.com/pruefwerk/spex/pkg/definition"
	"github.com/pruefwerk/spex/pkg/scenario"
)

// Legacy CLI adapters share authoring implementation with receiver hosts.
func loadDefinitions(inlineFile string, files []string, root string, read func(string) ([]byte, error), request bool) (*scenario.Scenario, []scenario.TestSource, error) {
	var inline *string
	if inlineFile != "" {
		data, err := read(inlineFile)
		if err != nil {
			return nil, nil, err
		}
		text := string(data)
		inline = &text
	}
	return definition.Load(inline, files, root, read, request)
}

func splitInlineSources(text string) ([]scenario.TestSource, error) {
	return definition.SplitInline(text)
}
func expandDefinitionFiles(root string, patterns []string) ([]string, error) {
	return definition.ExpandFiles(root, patterns)
}
