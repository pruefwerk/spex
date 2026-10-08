package engine

import (
	"errors"
	"path/filepath"

	"github.com/pruefwerk/spex/pkg/ownedkind"
)

// Bind the generic Kind mechanism to the testbench's private scope and fixed
// topology. Service setup and image selection remain runtime-owned policy.
func (h *Host) nativeKind(root, scopePath string, runner ownedkind.Runner) (*ownedkind.Cluster, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, errors.New("invalid runtime root")
	}
	scope, err := h.resolver.LoadScope(scopePath)
	if err != nil || scope.Target != "kind" {
		return nil, errors.New("native Kind requires a valid private Kind scope")
	}
	name, err := scope.Name()
	if err != nil || filepath.Clean(scopePath) != filepath.Join(root, ".spex", "execution-scopes", name+".json") {
		return nil, errors.New("Kind scope does not belong to this execution")
	}
	state := filepath.Join(root, ".spex", "isolated", name)
	if runner == nil {
		runner = ownedkind.CommandRunner{Directory: root}
	}
	return ownedkind.New(ownedkind.Config{Name: name, StateDirectory: state, Kubeconfig: filepath.Join(state, "kubeconfig"), ClusterConfig: filepath.Join(root, h.config.Topology), ImageRepository: h.config.ImageRepository, Runner: runner})
}
