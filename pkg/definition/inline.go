package definition

import (
	"fmt"
	"strings"

	"github.com/pruefwerk/spex/pkg/scenario"
)

// SplitInline treats a standalone, column-zero --- as an authoring
// boundary. Indented markers remain source content. The runtime still owns
// format detection and parsing for each resulting source.
func SplitInline(input string) ([]scenario.TestSource, error) {
	input = strings.ReplaceAll(strings.ReplaceAll(input, "\r\n", "\n"), "\r", "\n")
	lines := strings.SplitAfter(input, "\n")
	var tests []scenario.TestSource
	var content strings.Builder
	start := 1
	separated := false
	appendSource := func() error {
		text := content.String()
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("inline document %d (line %d): source is empty", len(tests)+1, start)
		}
		tests = append(tests, scenario.TestSource{Inline: &text})
		content.Reset()
		return nil
	}
	for index, line := range lines {
		if strings.TrimRight(line, " \t\n") != "---" {
			content.WriteString(line)
			continue
		}
		// Accept a single opening YAML document marker, but never silently
		// discard an empty document between or after separators.
		if !separated && len(tests) == 0 && strings.TrimSpace(content.String()) == "" {
			content.Reset()
		} else if err := appendSource(); err != nil {
			return nil, err
		}
		separated = true
		start = index + 2
	}
	if err := appendSource(); err != nil {
		return nil, err
	}
	return tests, nil
}
