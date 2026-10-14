package spex

import (
	"errors"
	"os"
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
	return NewReceiverMigrationRuntimeWithSources(repositoryRoot, suitePath, release, ReceiverMigrationSources{})
}

type ReceiverMigrationSources struct {
	Directory           string
	RequiredNamespace   string
	RequiredKubeContext string
	RepositoryConfig    string
}

func NewReceiverMigrationRuntimeWithSources(repositoryRoot, suitePath, release string, sources ReceiverMigrationSources) (receiver.Implementation, error) {
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
	var defaults scenario.RawRuntimeConfig
	if sources.RepositoryConfig != "" {
		path, err := scenario.SourcePath(root, sources.RepositoryConfig)
		if err != nil {
			return nil, errors.New("repository defaults unavailable")
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 1<<20 {
			return nil, errors.New("repository defaults unavailable")
		}
		defaults, err = scenario.ParseRuntimeConfig(data)
		if err != nil {
			return nil, errors.New("invalid repository defaults")
		}
		if _, err := decodeMigrationOverlay(defaults); err != nil {
			return nil, errors.New("invalid repository defaults")
		}
	}
	return releasedMigrationRuntime{migrationRuntime{flags: flags, sourceRoot: sources.Directory, requiredNamespace: sources.RequiredNamespace, requiredKubeContext: sources.RequiredKubeContext, defaults: defaults}, release}, nil
}
