package definition

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var namePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var kindTag = regexp.MustCompile(`\[(?:kind|kind-scenarios):([^\]]*)\]`)
var groupTag = regexp.MustCompile(`\[kind-group:([^\]]*)\]`)
var featureTags = regexp.MustCompile(`(?m)^\s*(@[^\n]+)$`)

func Names(value string) ([]string, error) {
	result := []string{}
	seen := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !namePattern.MatchString(part) {
			return nil, errors.New("use comma-separated lowercase names with optional hyphens")
		}
		if !seen[part] {
			result = append(result, part)
			seen[part] = true
		}
	}
	return result, nil
}

type Selection struct {
	Kinds  string `json:"kinds"`
	Groups string `json:"groups"`
}

// Commit metadata selects tests, never an execution environment or runtime mode.
func Select(event, message, kinds, groups string) (Selection, error) {
	if event != "workflow_dispatch" {
		kinds, groups = "", ""
		if match := kindTag.FindStringSubmatch(message); match != nil {
			kinds = match[1]
		}
		if match := groupTag.FindStringSubmatch(message); match != nil {
			groups = match[1]
		}
	}
	selected, err := Names(kinds)
	if err != nil {
		return Selection{}, err
	}
	if len(selected) == 0 {
		selected = []string{"all"}
	}
	for _, kind := range selected {
		if kind == "all" && len(selected) != 1 {
			return Selection{}, errors.New("select all or specific kinds, not both")
		}
	}
	requested, err := Names(groups)
	if err != nil {
		return Selection{}, err
	}
	if len(requested) > 0 && len(selected) == 1 && selected[0] == "all" {
		return Selection{}, errors.New("select a specific kind when selecting groups")
	}
	return Selection{strings.Join(selected, ","), strings.Join(requested, ",")}, nil
}

type TestSelection struct {
	Tags []string `json:"tags"`
}

func GroupFlags(groups string, tests []TestSelection, declared map[string]bool) ([]string, error) {
	requested, err := Names(groups)
	if err != nil {
		return nil, err
	}
	available := map[string]bool{}
	for _, test := range tests {
		for _, tag := range test.Tags {
			if strings.HasPrefix(tag, "group-") {
				available[strings.TrimPrefix(tag, "group-")] = true
			}
		}
	}
	unknown, empty := []string{}, []string{}
	for _, group := range requested {
		if !available[group] && !declared[group] {
			unknown = append(unknown, group)
		} else if !available[group] {
			empty = append(empty, group)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown groups: %s", strings.Join(unknown, ", "))
	}
	if len(empty) > 0 {
		sort.Strings(empty)
		return nil, fmt.Errorf("groups contain no runnable scenarios: %s", strings.Join(empty, ", "))
	}
	if len(requested) == 0 {
		return []string{}, nil
	}
	tags := make([]string, len(requested))
	for i, group := range requested {
		tags[i] = "group-" + group
	}
	return []string{"--include-any-tag", strings.Join(tags, ",")}, nil
}

func DeclaredGroups(suite string) (map[string]bool, error) {
	index := strings.TrimSuffix(suite, filepath.Ext(suite)) + ".groups.json"
	if _, err := os.Stat(index); os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	var definitions map[string]string
	if err := readDocument(index, &definitions, true); err != nil {
		return nil, err
	}
	if definitions == nil {
		return nil, errors.New("group index must map names to feature paths")
	}
	result := map[string]bool{}
	for name, feature := range definitions {
		names, err := Names(name)
		if err != nil || len(names) != 1 || names[0] != name {
			return nil, errors.New("invalid group definition")
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(index), feature))
		if err != nil {
			return nil, errors.New("group feature does not exist")
		}
		found := false
		for _, line := range featureTags.FindAllString(string(data), -1) {
			for _, token := range strings.Fields(line) {
				if token == "@group-"+name {
					found = true
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("group feature lacks @group-%s", name)
		}
		result[name] = true
	}
	return result, nil
}
