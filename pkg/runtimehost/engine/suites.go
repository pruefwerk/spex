package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
	"github.com/pruefwerk/spex/pkg/scenario"
)

var errSuiteNotConfigured = errors.New("suite has no qualified execution-owned lifecycle")

func (h *Host) forSuite(suite string) (*Host, error) {
	if suite == "" || suite == h.config.Suite {
		return h, nil
	}
	d, err := h.config.SuiteResources(suite)
	if err != nil {
		return nil, errSuiteNotConfigured
	}
	root := filepath.Dir(h.path)
	path, err := scenario.SourcePath(root, suite)
	if err != nil {
		return nil, err
	}
	document, err := readYAML(path)
	if err != nil {
		return nil, err
	}
	for key, target := range map[string]*string{"bindingRef": &d.Binding, "integrationProfileRef": &d.Profile} {
		value, err := scalarAt(document, "spec", key)
		if err != nil {
			return nil, err
		}
		if _, err := scenario.SourcePath(root, value.Value); err != nil {
			return nil, err
		}
		*target = value.Value
	}
	profile, err := readYAML(filepath.Join(root, d.Profile))
	if err != nil {
		return nil, err
	}
	topology, err := scalarAt(profile, "spec", "kind", "config")
	if err != nil {
		return nil, err
	}
	if _, err := scenario.SourcePath(root, topology.Value); err != nil {
		return nil, err
	}
	d.Topology = topology.Value
	resolver, err := hostconfig.NewResolver(d)
	if err != nil {
		return nil, err
	}
	return &Host{config: resolver.Snapshot(), resolver: resolver, path: h.path, version: h.version}, nil
}

// Persist the original trusted suite separately from SPEX_SUITE, which becomes
// an execution-owned projection. Profile children and later CI steps must choose
// the same host policy before validating the projected suite and scope.
func (h *Host) forOperation(args []string) (*Host, error) {
	suite := os.Getenv(hostconfig.SuiteEnvironment)
	if suite == "" && h.registeredSuite(os.Getenv("SPEX_SUITE")) {
		suite = os.Getenv("SPEX_SUITE")
	}
	if len(args) > 0 && (args[0] == "execution" || args[0] == "ci") {
		for i := 1; i < len(args); i++ {
			value := ""
			if args[i] == "--suite" && i+1 < len(args) {
				value = args[i+1]
			} else if strings.HasPrefix(args[i], "--suite=") {
				value = strings.TrimPrefix(args[i], "--suite=")
			}
			if h.registeredSuite(value) {
				suite = value
			}
		}
	}
	return h.forSuite(suite)
}

func (h *Host) registeredSuite(suite string) bool {
	if suite == h.config.Suite {
		return true
	}
	for _, policy := range h.config.Suites {
		if suite == policy.Suite {
			return true
		}
	}
	return false
}
