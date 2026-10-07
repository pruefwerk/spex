package migrationtestbench

import (
	"github.com/pruefwerk/spex/internal/migrationresources"
	"github.com/pruefwerk/spex/pkg/receiver"
)

type ResourceContract = migrationresources.ResourceContract
type SafetyVerifier = migrationresources.SafetyVerifier

// WithResourceContract attaches trusted, runtime-resolved physical claims to the
// existing suite implementation. The receiver separately supplies the coordinator.
func WithResourceContract(base receiver.Implementation, contract ResourceContract, verify SafetyVerifier) (receiver.Implementation, error) {
	return migrationresources.WithResourceContract(base, contract, verify)
}
