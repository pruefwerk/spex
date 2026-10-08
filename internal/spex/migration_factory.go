package spex

import (
	"errors"
	"path/filepath"

	"github.com/pruefwerk/spex/pkg/receiver"
	"github.com/pruefwerk/spex/pkg/scenario"
)

type releasedMigrationRuntime struct {
	migrationRuntime
	release string
}

func (r releasedMigrationRuntime) Release() string { return r.release }

// NewReceiverMigrationRuntime wraps the existing resolver/executor. All values
// are trusted host configuration; never derive these paths from submitted TOML.
// The host owns execution admission for suites with execution.requireHost. It
// must supply the appropriate resource contract, coordinator and safety checks
// before exposing such a runtime to callers. This factory supplies no policy.
func NewReceiverMigrationRuntime(repositoryRoot, suitePath, release string) (receiver.Implementation, error) {
	if release == "" {
		return nil, errors.New("receiver runtime requires an explicit release")
	}
	if err := scenario.ValidateGeneric(scenario.Scenario{Schema: scenario.Schema, Runtime: "migration-testbench/v1", RuntimeRelease: release}); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(suitePath) {
		suitePath = filepath.Join(root, suitePath)
	}
	flags, err := parseSuiteFlags("run", []string{"--suite", suitePath})
	if err != nil {
		return nil, err
	}
	flags.repoRoot = root
	flags.hostManaged = true
	return releasedMigrationRuntime{migrationRuntime{flags: flags}, release}, nil
}
