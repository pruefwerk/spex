// Package ownedkind manages a private Kind cluster on a shared Docker daemon.
// Hosts supply configuration and ownership identity; this package never selects
// services, credentials, topology defaults or application resource claims.
package ownedkind

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Runner interface {
	Run(context.Context, ...string) (string, error)
}
type Config struct {
	Name            string
	StateDirectory  string
	Kubeconfig      string
	ClusterConfig   string
	ImageRepository string
	Runner          Runner
}
type Cluster struct {
	config Config
	run    Runner
}

var namePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
var nodePattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func New(config Config) (*Cluster, error) {
	if !namePattern.MatchString(config.Name) || len(config.Name) > 53 || !filepath.IsAbs(config.StateDirectory) || filepath.Base(config.StateDirectory) != config.Name || config.Kubeconfig != filepath.Join(config.StateDirectory, "kubeconfig") || !filepath.IsAbs(config.ClusterConfig) || !regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`).MatchString(config.ImageRepository) {
		return nil, errors.New("invalid private Kind configuration")
	}
	if config.Runner == nil {
		config.Runner = CommandRunner{}
	}
	return &Cluster{config: config, run: config.Runner}, nil
}

type operationError struct {
	message string
	cause   error
}

func (e *operationError) Error() string { return e.message }
func (e *operationError) Unwrap() error { return e.cause }

func (c *Cluster) call(ctx context.Context, args ...string) (string, error) {
	output, err := c.run.Run(ctx, args...)
	if err != nil {
		return "", &operationError{"Kind resource operation failed; inspect ownership evidence", err}
	}
	return strings.TrimSpace(output), nil
}

func (c *Cluster) checkConfig() error {
	data, err := os.ReadFile(c.config.ClusterConfig)
	if err != nil {
		return errors.New("Kind configuration unavailable")
	}
	var config struct {
		Kind    string `yaml:"kind"`
		Version string `yaml:"apiVersion"`
		Name    string `yaml:"name"`
		Nodes   []struct {
			Role string `yaml:"role"`
		} `yaml:"nodes"`
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if decoder.Decode(&config) != nil || config.Kind != "Cluster" || config.Version != "kind.x-k8s.io/v1alpha4" || len(config.Nodes) != 1 || config.Nodes[0].Role != "control-plane" {
		return errors.New("private Kind requires one control-plane without host mounts or fixed ports")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("private Kind configuration must contain one document")
	}
	return nil
}

func (c *Cluster) nodeIDs(ctx context.Context) ([]string, error) {
	output, err := c.call(ctx, "docker", "ps", "-aq", "--no-trunc", "--filter", "label=io.x-k8s.kind.cluster="+c.config.Name)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(output)
	seen := map[string]bool{}
	for _, id := range ids {
		if !nodePattern.MatchString(id) || seen[id] {
			return nil, errors.New("invalid Docker node identity")
		}
		seen[id] = true
	}
	return ids, nil
}

func (c *Cluster) inspectNode(ctx context.Context, id string) error {
	if !nodePattern.MatchString(id) {
		return errors.New("invalid Docker node identity")
	}
	output, err := c.call(ctx, "docker", "inspect", id)
	if err != nil {
		return err
	}
	var records []struct {
		ID     string `json:"Id"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if json.Unmarshal([]byte(output), &records) != nil || len(records) != 1 || records[0].ID != id || records[0].Config.Labels["io.x-k8s.kind.cluster"] != c.config.Name {
		return errors.New("Docker node ownership mismatch")
	}
	return nil
}

func (c *Cluster) recordNodes(ctx context.Context) error {
	ids, err := c.nodeIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := c.inspectNode(ctx, id); err != nil {
			return err
		}
	}
	if ids == nil {
		ids = []string{}
	}
	return writeNew(filepath.Join(c.config.StateDirectory, "nodes.json"), ids)
}

func (c *Cluster) Create(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := c.checkConfig(); err != nil {
		return err
	}
	var configured struct {
		Scope string `json:"scope"`
		Suite string `json:"suite"`
	}
	if readLedger(filepath.Join(c.config.StateDirectory, "configuration.json"), &configured) != nil || configured.Scope != c.config.Name || configured.Suite == "" {
		return errors.New("configure and validate the private suite before cluster creation")
	}
	if err := writeNew(filepath.Join(c.config.StateDirectory, "creation.json"), map[string]string{"scope": c.config.Name}); err != nil {
		return errors.New("cluster creation already attempted or cannot be recorded")
	}
	ids, err := c.nodeIDs(ctx)
	if err != nil {
		return err
	}
	if len(ids) != 0 {
		return errors.New("cluster already exists; refusing adoption")
	}
	_, primary := c.call(ctx, "kind", "create", "cluster", "--name", c.config.Name, "--config", c.config.ClusterConfig, "--kubeconfig", c.config.Kubeconfig, "--wait", "120s")
	// Cancellation cannot prevent capture of nodes from an attempted creation.
	capture, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	if err := c.recordNodes(capture); err != nil {
		if primary != nil {
			return &operationError{"Kind creation and node capture incomplete; manual ledger review required", primary}
		}
		return errors.New("node ledger capture incomplete; manual review required")
	}
	return primary
}

func (c *Cluster) OwnedNodes(ctx context.Context) ([]string, error) {
	var recorded []string
	if err := readLedger(filepath.Join(c.config.StateDirectory, "nodes.json"), &recorded); err != nil {
		return nil, err
	}
	if recorded == nil {
		return nil, errors.New("invalid node ledger")
	}
	known := map[string]bool{}
	for _, id := range recorded {
		if !nodePattern.MatchString(id) || known[id] {
			return nil, errors.New("invalid node ledger")
		}
		known[id] = true
	}
	current, err := c.nodeIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range current {
		if !known[id] {
			return nil, errors.New("unrecorded cluster nodes; cleanup requires review")
		}
	}
	for _, id := range current {
		if err := c.inspectNode(ctx, id); err != nil {
			return nil, err
		}
	}
	return current, nil
}

func (c *Cluster) Cleanup(ctx context.Context) error {
	if _, err := os.Lstat(filepath.Join(c.config.StateDirectory, "nodes.json")); os.IsNotExist(err) {
		if _, err := os.Lstat(filepath.Join(c.config.StateDirectory, "creation.json")); os.IsNotExist(err) {
			return nil
		}
		return errors.New("creation interrupted without node ledger; manual review required")
	}
	nodes, err := c.OwnedNodes(ctx)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if _, err := c.call(ctx, "docker", "rm", "--force", "--volumes", node); err != nil {
			return err
		}
	}
	// Independent image uncertainty must not prevent deletion of verified nodes.
	// Retain incomplete image evidence and return failure so capacity stays held.
	images, ledgerErr := c.imageRecords()
	for _, record := range images {
		present, err := c.call(ctx, "docker", "image", "ls", "--quiet", record.Tag)
		if err != nil {
			return err
		}
		if present == "" {
			continue
		}
		actual, err := c.call(ctx, "docker", "image", "inspect", record.Tag, "--format", "{{.Id}}")
		if err != nil {
			return err
		}
		if actual != record.ID {
			return errors.New("image ownership changed; refusing deletion")
		}
		if _, err := c.call(ctx, "docker", "image", "rm", record.Tag); err != nil {
			return err
		}
	}
	return ledgerErr
}
