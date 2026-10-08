package migrationtestbench

import (
	"context"
	"io"

	"github.com/pruefwerk/spex/internal/spex"
)

type SuiteTest = spex.SuiteTest

// DiscoverSuite uses the engine's existing inherited configuration and discovery.
// It neither creates jobs nor changes the acceptance environment.
func DiscoverSuite(ctx context.Context, path string) ([]SuiteTest, error) {
	return spex.DiscoverSuite(ctx, path)
}

func ValidateSuite(ctx context.Context, path string, tags []string, out io.Writer) error {
	return spex.ValidateSuite(ctx, path, tags, out)
}

// GroupSummary preserves the same report interpretation as `spex reports groups`.
func GroupSummary(root, title string, out io.Writer) error {
	return spex.GroupSummary(root, title, out)
}
