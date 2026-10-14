// Package migrationtestbench exposes the existing suite runtime to receiver
// hosts without moving configuration defaults or service topology into Spex.
package migrationtestbench

import (
	"github.com/pruefwerk/spex/internal/spex"
	"github.com/pruefwerk/spex/pkg/receiver"
	"github.com/pruefwerk/spex/pkg/scenario"
)

type Config struct {
	RepositoryRoot      string
	SuitePath           string
	Release             string
	SourceRoot          string
	RequiredNamespace   string
	RequiredKubeContext string
	RepositoryConfig    string
}

func ValidateReceiverConfig(raw scenario.RawRuntimeConfig, allowedFields []string) error {
	return spex.ValidateReceiverMigrationConfig(raw, allowedFields)
}

// New binds a trusted, preinstalled configuration release. It does not download
// or authenticate that release; the hosting runtime repository owns that policy.
func New(config Config) (receiver.Implementation, error) {
	return spex.NewReceiverMigrationRuntimeWithSources(config.RepositoryRoot, config.SuitePath, config.Release, spex.ReceiverMigrationSources{Directory: config.SourceRoot, RequiredNamespace: config.RequiredNamespace, RequiredKubeContext: config.RequiredKubeContext, RepositoryConfig: config.RepositoryConfig})
}
