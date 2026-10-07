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
	return releasedMigrationRuntime{migrationRuntime{flags: flags}, release}, nil
}
