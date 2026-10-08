package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
	"gopkg.in/yaml.v3"
)

// Project only the execution-owned fields. Preserve the existing documents,
// rather than maintaining a second suite/binding/profile configuration model.
func (h *Host) configureKind(root, scopePath string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if _, err := h.nativeKind(root, scopePath, nil); err != nil {
		return "", err
	}
	scope, err := h.resolver.LoadScope(scopePath)
	if err != nil {
		return "", err
	}
	name, _ := scope.Name()
	if err := checkKindTopology(filepath.Join(root, h.config.Topology)); err != nil {
		return "", err
	}
	suite, err := readYAML(filepath.Join(root, h.config.Suite))
	if err != nil {
		return "", err
	}
	bindingRef, err := scalarAt(suite, "spec", "bindingRef")
	if err != nil {
		return "", err
	}
	profileRef, err := scalarAt(suite, "spec", "integrationProfileRef")
	if err != nil {
		return "", err
	}
	// These are trusted runtime templates, not caller-selected setup documents.
	if bindingRef.Value != h.config.Binding || profileRef.Value != h.config.Profile {
		return "", errors.New("unrecognized Kind template references")
	}
	binding, err := readYAML(filepath.Join(root, bindingRef.Value))
	if err != nil {
		return "", err
	}
	profile, err := readYAML(filepath.Join(root, profileRef.Value))
	if err != nil {
		return "", err
	}
	context, err := scalarAt(binding, "spec", "kubeContext")
	if err != nil {
		return "", err
	}
	start, err := nodeAt(profile, "spec", "kind", "start")
	if err != nil || start.Kind != yaml.ScalarNode || start.Tag != "!!bool" {
		return "", errors.New("Kind start must be a boolean")
	}
	cluster, err := scalarAt(profile, "spec", "kind", "clusterName")
	if err != nil {
		return "", err
	}
	config, err := scalarAt(profile, "spec", "kind", "config")
	if err != nil || config.Value != h.config.Topology {
		return "", errors.New("unrecognized Kind topology reference")
	}
	// The existing resolver accepts profile-relative references. Point at the
	// same topology without depending on the process's working directory.
	config.Value = filepath.Base(h.config.Topology)
	commands, err := nodeAt(profile, "spec", "kind", "commands")
	if err != nil || commands.Kind != yaml.SequenceNode {
		return "", errors.New("Kind commands missing")
	}
	var preparation *yaml.Node
	for _, entry := range commands.Content {
		command, err := scalarAt(entry, "command")
		if err != nil {
			return "", err
		}
		if strings.Contains(command.Value, "/.spex/bin/runtime-host' kind images ") {
			if preparation != nil {
				return "", errors.New("multiple Kind preparation commands")
			}
			preparation = command
		}
	}
	if preparation == nil {
		return "", errors.New("unrecognized Kind preparation profile")
	}
	workspace, err := scalarAt(suite, "spec", "workspaceDir")
	if err != nil {
		return "", err
	}
	reports, err := scalarAt(suite, "spec", "reports", "outputDir")
	if err != nil {
		return "", err
	}
	context.Value, cluster.Value = "kind-"+name, name
	start.Value, start.Tag = "false", "!!bool"
	preparation.Value = "'${repoRoot}/.spex/bin/runtime-host' kind images --root '${repoRoot}' --scope " + shellQuote(scopePath)
	bindingRef.Value, profileRef.Value = "bindings/"+name+".yaml", "integration/"+name+".yaml"
	workspace.Value, reports.Value = ".spex/isolated/"+name+"/workspaces", ".spex/isolated/"+name+"/reports"
	var groups []byte
	groupsPath := filepath.Join(root, strings.TrimSuffix(h.config.Suite, filepath.Ext(h.config.Suite))+".groups.json")
	if data, err := os.ReadFile(groupsPath); err == nil {
		if len(data) > 4<<20 || !json.Valid(data) {
			return "", errors.New("invalid suite groups")
		}
		groups = data
	} else if !os.IsNotExist(err) {
		return "", err
	}
	state := filepath.Join(root, ".spex/isolated", name)
	if err := os.MkdirAll(filepath.Dir(state), 0700); err != nil {
		return "", err
	}
	if err := os.Mkdir(state, 0700); err != nil {
		return "", err
	}
	for _, item := range []struct {
		path  string
		value *yaml.Node
	}{{bindingRef.Value, binding}, {profileRef.Value, profile}, {name + ".yaml", suite}} {
		data, err := yaml.Marshal(item.value)
		if err != nil {
			return "", err
		}
		if err := writePrivate(filepath.Join(root, item.path), data); err != nil {
			return "", err
		}
	}
	if groups != nil {
		if err := writePrivate(filepath.Join(root, name+".groups.json"), groups); err != nil {
			return "", err
		}
	}
	data, _ := json.Marshal(struct {
		Scope string `json:"scope"`
		Suite string `json:"suite"`
	}{name, name + ".yaml"})
	if err := writePrivate(filepath.Join(state, "configuration.json"), data); err != nil {
		return "", err
	}
	policy, _ := json.Marshal(struct {
		Identity string `json:"identity"`
	}{hostconfig.Identity(h.config)})
	if err := writePrivate(filepath.Join(state, "host-policy.json"), policy); err != nil {
		return "", err
	}
	return filepath.Join(root, name+".yaml"), nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func writePrivate(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func readYAML(path string) (*yaml.Node, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("runtime YAML unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, errors.New("runtime YAML invalid")
	}
	decoder := yaml.NewDecoder(io.LimitReader(f, (4<<20)+1))
	var doc, extra yaml.Node
	if decoder.Decode(&doc) != nil || decoder.Decode(&extra) != io.EOF || len(doc.Content) != 1 {
		return nil, errors.New("expected one runtime YAML document")
	}
	if err := validateYAML(&doc, 0); err != nil {
		return nil, err
	}
	return &doc, nil
}

func validateYAML(n *yaml.Node, depth int) error {
	if depth > 64 {
		return errors.New("runtime YAML nesting exceeds limit")
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.New("runtime YAML aliases are not supported")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
				return errors.New("invalid or duplicate runtime YAML key")
			}
			seen[key.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := validateYAML(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func nodeAt(n *yaml.Node, path ...string) (*yaml.Node, error) {
	if n.Kind == yaml.DocumentNode {
		n = n.Content[0]
	}
	for _, key := range path {
		if n.Kind != yaml.MappingNode {
			return nil, errors.New("runtime YAML field missing")
		}
		var next *yaml.Node
		for i := 0; i < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				next = n.Content[i+1]
				break
			}
		}
		if next == nil {
			return nil, errors.New("runtime YAML field missing")
		}
		n = next
	}
	return n, nil
}

func scalarAt(n *yaml.Node, path ...string) (*yaml.Node, error) {
	value, err := nodeAt(n, path...)
	if err != nil || value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
		return nil, errors.New("runtime YAML string missing")
	}
	return value, nil
}

func checkKindTopology(path string) error {
	node, err := readYAML(path)
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	var topology struct {
		Kind    string `yaml:"kind"`
		Version string `yaml:"apiVersion"`
		Name    string `yaml:"name"`
		Nodes   []struct {
			Role string `yaml:"role"`
		} `yaml:"nodes"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if decoder.Decode(&topology) != nil || topology.Kind != "Cluster" || topology.Version != "kind.x-k8s.io/v1alpha4" || len(topology.Nodes) != 1 || topology.Nodes[0].Role != "control-plane" {
		return errors.New("isolated Kind requires one control-plane without mounts or fixed ports")
	}
	return nil
}
