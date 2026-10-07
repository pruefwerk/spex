// Package receiverexample shows the boundary a runtime repository implements.
// It supplies no production admission policy, runtime, credentials or setup.
package receiverexample

import (
	"context"
	"github.com/pruefwerk/spex/pkg/receiver"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

// Run is called by the runtime repository's own executable after authentication,
// artifact identity checks and immutable source checkout. A host may DecodeArtifact
// earlier, before fetching source, using the same authenticated Expected values.
func Run(ctx context.Context, artifact []byte, expected receiver.Expected, checkout receiver.Checkout, host receiver.Host, sink scenarioruntime.ArtifactSink) int {
	request, err := receiver.DecodeArtifact(artifact, expected)
	if err != nil {
		return 1
	}
	receipt, err := receiver.Execute(ctx, request, checkout, host, sink)
	return receiver.ExitCode(receipt, err)
}
