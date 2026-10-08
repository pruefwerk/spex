package ownedkind

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type daemon struct {
	nodes           map[string]string
	images          map[string]string
	calls           [][]string
	createErr       error
	cancel          context.CancelFunc
	wrongInspection bool
}

func nodeID(name string) string {
	hash := sha256.Sum256([]byte(name))
	return hex.EncodeToString(hash[:])
}
func argument(args []string, key string) string {
	for i, value := range args {
		if value == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
func (d *daemon) Run(ctx context.Context, args ...string) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	d.calls = append(d.calls, append([]string{}, args...))
	switch {
	case args[0] == "kind" && args[1] == "create":
		name := argument(args, "--name")
		d.nodes[nodeID(name)] = name
		if d.cancel != nil {
			d.cancel()
			return "", context.Canceled
		}
		return "", d.createErr
	case args[0] == "docker" && args[1] == "ps":
		name := strings.TrimPrefix(argument(args, "--filter"), "label=io.x-k8s.kind.cluster=")
		ids := []string{}
		for id, owner := range d.nodes {
			if owner == name {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		return strings.Join(ids, "\n"), nil
	case args[0] == "docker" && args[1] == "inspect":
		owner := d.nodes[args[2]]
		if d.wrongInspection {
			owner = "different-owner"
		}
		data, _ := json.Marshal([]any{map[string]any{"Id": args[2], "Config": map[string]any{"Labels": map[string]string{"io.x-k8s.kind.cluster": owner}}}})
		return string(data), nil
	case args[0] == "docker" && args[1] == "rm":
		delete(d.nodes, args[len(args)-1])
		return "", nil
	case args[0] == "docker" && args[1] == "pull":
		d.images[args[2]] = "sha256:" + nodeID(args[2])
		return "", nil
	case args[0] == "docker" && args[1] == "tag":
		d.images[args[3]] = args[2]
		return "", nil
	case args[0] == "docker" && args[1] == "build":
		tag := argument(args, "-t")
		d.images[tag] = "sha256:" + nodeID(tag)
		return "", nil
	case args[0] == "docker" && args[1] == "image" && args[2] == "ls":
		return d.images[args[len(args)-1]], nil
	case args[0] == "docker" && args[1] == "image" && args[2] == "inspect":
		return d.images[args[3]], nil
	case args[0] == "docker" && args[1] == "image" && args[2] == "rm":
		delete(d.images, args[3])
		return "", nil
	case args[0] == "kind" && args[1] == "load":
		return "", nil
	case args[0] == "docker" && args[1] == "exec":
		return "", nil
	default:
		return "", errors.New("unexpected fixture command")
	}
}

func fixture(t *testing.T, name string, d *daemon) *Cluster {
	t.Helper()
	if d.nodes == nil {
		d.nodes = map[string]string{}
	}
	if d.images == nil {
		d.images = map[string]string{}
	}
	root := t.TempDir()
	state := filepath.Join(root, name)
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "kind.yaml")
	if err := os.WriteFile(config, []byte("kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnodes:\n- role: control-plane\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(filepath.Join(state, "configuration.json"), map[string]string{"scope": name, "suite": name + ".yaml"}); err != nil {
		t.Fatal(err)
	}
	cluster, err := New(Config{Name: name, StateDirectory: state, Kubeconfig: filepath.Join(state, "kubeconfig"), ClusterConfig: config, ImageRepository: "fixture", Runner: d})
	if err != nil {
		t.Fatal(err)
	}
	return cluster
}

func TestTwoClustersShareDaemonWithoutDeletingEachOther(t *testing.T) {
	d := &daemon{}
	a := fixture(t, "worker-a", d)
	b := fixture(t, "worker-b", d)
	ctx := context.Background()
	for _, cluster := range []*Cluster{a, b} {
		if err := cluster.Create(ctx); err != nil {
			t.Fatal(err)
		}
		if err := cluster.LoadImage(ctx, Image{Alias: "fixture-probe:kind", Source: "registry.example/probe:commit"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.nodes[nodeID("worker-b")]; !ok {
		t.Fatal("deleted another execution")
	}
	otherTag, _ := b.imageTag("fixture-probe:kind")
	if d.images[otherTag] == "" || d.images["registry.example/probe:commit"] == "" {
		t.Fatal("deleted shared or foreign image")
	}
	if err := a.Cleanup(ctx); err != nil {
		t.Fatal("repeat cleanup failed", err)
	}
	if err := b.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	for _, args := range d.calls {
		if reflect.DeepEqual(args[:2], []string{"docker", "tag"}) && !strings.HasPrefix(args[3], "fixture:worker-") {
			t.Fatal("shared alias mutated")
		}
		if reflect.DeepEqual(args[:2], []string{"kind", "delete"}) || strings.Contains(strings.Join(args, " "), "prune") {
			t.Fatal("broad cleanup command")
		}
	}
}

func TestPreexistingClusterIsNeverAdopted(t *testing.T) {
	d := &daemon{}
	c := fixture(t, "worker-a", d)
	d.nodes[nodeID("preexisting")] = c.config.Name
	if err := c.Create(context.Background()); err == nil {
		t.Fatal("adopted cluster")
	}
	if err := c.Cleanup(context.Background()); err == nil {
		t.Fatal("accepted missing ledger")
	}
	if len(d.nodes) != 1 {
		t.Fatal("deleted unowned node")
	}
}

func TestUnrecordedOrMismatchedNodesBlockCleanupBeforeDeletion(t *testing.T) {
	for _, change := range []string{"unrecorded", "label"} {
		d := &daemon{}
		c := fixture(t, "worker-a", d)
		ctx := context.Background()
		if err := c.Create(ctx); err != nil {
			t.Fatal(err)
		}
		if change == "unrecorded" {
			d.nodes[nodeID("extra")] = c.config.Name
		} else {
			d.wrongInspection = true
		}
		before := len(d.nodes)
		if err := c.Cleanup(ctx); err == nil {
			t.Fatal("unsafe cleanup accepted")
		}
		if len(d.nodes) != before {
			t.Fatal("deleted node before full verification")
		}
	}
}

func TestPartialCreationAndCancellationCaptureNodes(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		d := &daemon{}
		c := fixture(t, "worker-a", d)
		ctx, cancel := context.WithCancel(context.Background())
		if cancelled {
			d.cancel = cancel
		} else {
			d.createErr = errors.New("PRIVATE_FAILURE")
		}
		err := c.Create(ctx)
		if err == nil || strings.Contains(err.Error(), "PRIVATE_FAILURE") {
			t.Fatal(err)
		}
		if cancelled && !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation identity lost", err)
		}
		cancel()
		var ledger []string
		if err := readLedger(filepath.Join(c.config.StateDirectory, "nodes.json"), &ledger); err != nil || len(ledger) != 1 {
			t.Fatal(ledger, err)
		}
		if err := c.Cleanup(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(d.nodes) != 0 {
			t.Fatal("partial cluster leaked")
		}
	}
}

func TestCreationLedgerIsPrivateAndCannotBeReopened(t *testing.T) {
	d := &daemon{}
	c := fixture(t, "worker-a", d)
	if err := c.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.Background()); err == nil {
		t.Fatal("reopened creation")
	}
	for _, name := range []string{"creation.json", "nodes.json"} {
		info, err := os.Stat(filepath.Join(c.config.StateDirectory, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal(name, err)
		}
	}
}

func TestInvalidTopologyAndAbsentLedgerFailClosed(t *testing.T) {
	d := &daemon{}
	c := fixture(t, "worker-a", d)
	if err := os.WriteFile(c.config.ClusterConfig, []byte("kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnodes:\n- role: control-plane\n  extraMounts: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.Background()); err == nil || len(d.calls) != 0 {
		t.Fatal("invalid topology accepted")
	}
	if err := c.Cleanup(context.Background()); err != nil {
		t.Fatal("cleanup without attempt failed", err)
	}
	if err := writeNew(filepath.Join(c.config.StateDirectory, "creation.json"), map[string]string{"scope": c.config.Name}); err != nil {
		t.Fatal(err)
	}
	if err := c.Cleanup(context.Background()); err == nil {
		t.Fatal("hard-killed creation accepted")
	}
}
