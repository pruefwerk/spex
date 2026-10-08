package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pruefwerk/spex/pkg/migrationtestbench"
	"github.com/pruefwerk/spex/pkg/ownedkind"
	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
	"github.com/pruefwerk/spex/pkg/scenario"
)

func (h *Host) ciGroupNames() []string { return h.config.Groups }

type groupEntry struct {
	Scenarios int    `json:"scenarios"`
	Selected  bool   `json:"selected"`
	Status    string `json:"status"`
}

type matrixEntry struct {
	Kind    string `json:"scenario"`
	Suite   string `json:"suite"`
	Output  string `json:"out"`
	Cluster string `json:"cluster"`
}

// Derive CI metadata from the existing suite/profile inventory. Cluster is a
// report label here; execution always allocates its own private resource name.
func (h *Host) kindMatrix(root, selected string) ([]matrixEntry, error) {
	paths, err := filepath.Glob(filepath.Join(root, "suite-kind-*.yaml"))
	if err != nil {
		return nil, err
	}
	entries := []matrixEntry{}
	index := map[string]matrixEntry{}
	for _, file := range paths {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(file), "suite-kind-"), ".yaml")
		names, err := hostconfig.Names(name)
		if err != nil || len(names) != 1 {
			return nil, errors.New("invalid suite inventory name")
		}
		suite, err := readYAML(file)
		if err != nil {
			return nil, err
		}
		ref, err := scalarAt(suite, "spec", "integrationProfileRef")
		if err != nil {
			return nil, err
		}
		profilePath, err := scenario.SourcePath(root, ref.Value)
		if err != nil {
			return nil, err
		}
		profile, err := readYAML(profilePath)
		if err != nil {
			return nil, err
		}
		cluster, err := scalarAt(profile, "spec", "kind", "clusterName")
		if err != nil {
			return nil, err
		}
		entry := matrixEntry{name, filepath.Base(file), ".spex/generated/kind/" + name, cluster.Value}
		entries = append(entries, entry)
		index[name] = entry
	}
	names, err := hostconfig.Names(selected)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 || (len(names) == 1 && names[0] == "all") {
		return entries, nil
	}
	entries = []matrixEntry{}
	for _, name := range names {
		entry, ok := index[name]
		if !ok {
			return nil, errors.New("unknown Kind suite")
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (h *Host) planGroups(tests []migrationtestbench.SuiteTest, requested string, declared map[string]bool) (map[string]groupEntry, error) {
	selection := make([]hostconfig.TestSelection, len(tests))
	for i, test := range tests {
		selection[i].Tags = test.Tags
	}
	if _, err := hostconfig.GroupFlags(requested, selection, declared); err != nil {
		return nil, err
	}
	plan := map[string]groupEntry{}
	for _, name := range h.ciGroupNames() {
		plan[name] = groupEntry{}
	}
	for name := range declared {
		if name == "baseline" {
			return nil, errors.New("baseline is not a declared feature group")
		}
		if _, ok := plan[name]; !ok {
			return nil, errors.New("workflow step missing for declared group")
		}
	}
	for _, test := range tests {
		group := ""
		baseline := false
		for _, tag := range test.Tags {
			if tag == h.config.BaselineTag {
				baseline = true
			}
			if strings.HasPrefix(tag, "group-") {
				if group != "" {
					return nil, errors.New("CI groups must not overlap")
				}
				group = strings.TrimPrefix(tag, "group-")
			}
		}
		if group == "" {
			if !baseline {
				return nil, errors.New("ungrouped scenario needs ci-baseline")
			}
			group = "baseline"
		} else if baseline {
			return nil, errors.New("grouped scenario also belongs to baseline")
		}
		entry, ok := plan[group]
		if !ok {
			return nil, errors.New("workflow step missing for scenario group")
		}
		entry.Scenarios++
		plan[group] = entry
	}
	names, _ := hostconfig.Names(requested)
	selected := map[string]bool{}
	for _, name := range names {
		selected[name] = true
	}
	for name, entry := range plan {
		entry.Selected = entry.Scenarios > 0 && (len(names) == 0 || selected[name])
		switch {
		case entry.Scenarios == 0:
			entry.Status = "unimplemented"
		case entry.Selected:
			entry.Status = "pending"
		default:
			entry.Status = "not selected"
		}
		plan[name] = entry
	}
	return plan, nil
}

func (h *Host) healthyResources(data string) bool {
	var resources struct {
		Items []struct {
			Metadata struct{ Name string }   `json:"metadata"`
			Spec     struct{ Replicas *int } `json:"spec"`
			Status   struct {
				Ready int `json:"readyReplicas"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal([]byte(data), &resources) != nil {
		return false
	}
	required := map[string]bool{}
	for _, name := range h.config.HealthResources {
		required[name] = true
	}
	for _, item := range resources.Items {
		replicas := 1
		if item.Spec.Replicas != nil {
			replicas = *item.Spec.Replicas
		}
		if item.Status.Ready < replicas {
			return false
		}
		delete(required, item.Metadata.Name)
	}
	return len(required) == 0
}

func appendCI(path, value string) error {
	if path == "" {
		return nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.WriteString(file, value)
	return err
}

func (h *Host) ciCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("CI operation required")
	}
	flags := flag.NewFlagSet("ci", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", ".", "report directory")
	suite := flags.String("suite", h.config.Suite, "suite input")
	requested := flags.String("groups", "", "selected groups")
	group := flags.String("group", "", "result group")
	status := flags.Int("status", 0, "exit status")
	config := flags.String("kubeconfig", "", "private kubeconfig")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return errors.New("invalid CI flags")
	}
	switch args[0] {
	case "matrix":
		root, err := filepath.Abs(*root)
		if err != nil {
			return err
		}
		matrix, err := h.kindMatrix(root, os.Getenv("KIND_SCENARIOS"))
		if err != nil {
			return err
		}
		data, err := json.Marshal(matrix)
		if err != nil {
			return err
		}
		return appendCI(os.Getenv("GITHUB_OUTPUT"), "scenarios="+string(data)+"\n")
	case "select":
		selection, err := hostconfig.Select(os.Getenv("GITHUB_EVENT_NAME"), os.Getenv("HEAD_COMMIT_MESSAGE"), os.Getenv("INPUT_KIND_SCENARIOS"), os.Getenv("INPUT_KIND_GROUPS"))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Selected kinds: %s; groups: %s\n", selection.Kinds, selection.Groups)
		return appendCI(os.Getenv("GITHUB_OUTPUT"), fmt.Sprintf("scenarios=%s\ngroups=%s\n", selection.Kinds, selection.Groups))
	case "plan", "validate":
		tests, err := migrationtestbench.DiscoverSuite(ctx, *suite)
		if err != nil {
			return err
		}
		declared, err := hostconfig.DeclaredGroups(*suite)
		if err != nil {
			return err
		}
		if args[0] == "validate" {
			selection := make([]hostconfig.TestSelection, len(tests))
			for i, test := range tests {
				selection[i].Tags = test.Tags
			}
			filtered, err := hostconfig.GroupFlags(*requested, selection, declared)
			if err != nil {
				return err
			}
			tags := []string{}
			if len(filtered) > 0 {
				tags = strings.Split(filtered[1], ",")
			}
			return migrationtestbench.ValidateSuite(ctx, *suite, tags, out)
		}
		plan, err := h.planGroups(tests, *requested, declared)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(*root, 0700); err != nil {
			return err
		}
		var outputs strings.Builder
		for _, name := range h.ciGroupNames() {
			if err := os.Remove(filepath.Join(*root, name+"-result.json")); err != nil && !os.IsNotExist(err) {
				return err
			}
			entry := plan[name]
			fmt.Fprintf(out, "%s: %d scenarios; %s\n", name, entry.Scenarios, entry.Status)
			fmt.Fprintf(&outputs, "%s=%t\n", name, entry.Selected)
		}
		data, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(*root, "group-plan.json"), append(data, '\n'), 0600); err != nil {
			return err
		}
		return appendCI(os.Getenv("GITHUB_OUTPUT"), outputs.String())
	case "result":
		known := false
		for _, name := range h.ciGroupNames() {
			if name == *group {
				known = true
			}
		}
		if !known || *status < 0 {
			return errors.New("invalid group result")
		}
		if err := os.MkdirAll(*root, 0700); err != nil {
			return err
		}
		result := struct {
			Status string `json:"status"`
			Code   int    `json:"exitCode"`
		}{"failed", *status}
		if *status == 0 {
			result.Status = "passed"
		}
		if *status == 124 {
			result.Status = "timed out"
		}
		data, _ := json.Marshal(result)
		return os.WriteFile(filepath.Join(*root, *group+"-result.json"), append(data, '\n'), 0600)
	case "summary":
		var rendered bytes.Buffer
		primary := migrationtestbench.GroupSummary(*root, h.config.SummaryTitle, &rendered)
		if rendered.Len() == 0 {
			return primary
		}
		if _, err := out.Write(rendered.Bytes()); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*root, "group-summary.md"), rendered.Bytes(), 0600); err != nil {
			return err
		}
		if err := appendCI(os.Getenv("GITHUB_STEP_SUMMARY"), rendered.String()); err != nil {
			return err
		}
		return primary
	case "health":
		if *config == "" {
			return errors.New("private kubeconfig required")
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		runner := ownedkind.CommandRunner{}
		command := []string{"kubectl", "--kubeconfig", *config, "--request-timeout=10s"}
		if _, err := runner.Run(ctx, append(append([]string{}, command...), "get", "--raw=/readyz")...); err != nil {
			return err
		}
		data, err := runner.Run(ctx, append(command, "-n", h.config.Namespace, "get", "deploy,statefulset", "-o", "json")...)
		if err != nil || !h.healthyResources(data) {
			return errors.New("shared infrastructure unhealthy")
		}
		return nil
	default:
		return errors.New("unknown CI operation")
	}
}
