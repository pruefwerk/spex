// Package migrationtestbench exposes the existing suite runtime to receiver
// hosts without moving configuration defaults or service topology into Spex.
package migrationtestbench

import (
	"github.com/pruefwerk/spex/internal/spex"
	"github.com/pruefwerk/spex/pkg/receiver"
)

type Config struct {
	RepositoryRoot string
	SuitePath      string
	Release        string
}

// New binds a trusted, preinstalled configuration release. It does not download
// or authenticate that release; the hosting runtime repository owns that policy.
func New(config Config) (receiver.Implementation, error) {
	return spex.NewReceiverMigrationRuntime(config.RepositoryRoot, config.SuitePath, config.Release)
}
