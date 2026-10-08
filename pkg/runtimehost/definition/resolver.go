package definition

import (
	"github.com/pruefwerk/spex/pkg/migrationtestbench"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

// Resolver owns a validated immutable policy snapshot. Separate hosts do not
// share configuration or execution identity through the compatibility globals.
type Resolver struct{ snapshot Definition }

func NewResolver(d Definition) (*Resolver, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &Resolver{snapshot: Clone(d)}, nil
}

func (r *Resolver) Snapshot() Definition        { return Clone(r.snapshot) }
func (r *Resolver) Protocol(kind string) string { return r.snapshot.SchemaPrefix + "." + kind + "/v1" }

// Compatibility entrypoints delegate to the same resolver. New hosts should
// retain their own Resolver instead of changing process-wide policy.
func currentResolver() *Resolver { return &Resolver{snapshot: Current()} }
func Allocate(repo string, run, attempt int64, request, target string) (Scope, error) {
	return currentResolver().Allocate(repo, run, attempt, request, target)
}
func LoadScope(path string) (Scope, error)       { return currentResolver().LoadScope(path) }
func Setup(tags []string) (SetupValues, error)   { return currentResolver().Setup(tags) }
func ReadSetup(path string) (SetupValues, error) { return currentResolver().ReadSetup(path) }
func ReadSourcePolicy(path string) (SourcePolicy, error) {
	return currentResolver().ReadSourcePolicy(path)
}
func ReadArtifacts(root string) (Artifacts, error) { return currentResolver().ReadArtifacts(root) }
func ResourceContract(scope Scope, id string, tests []scenarioruntime.TestDescription, policy *ResourcePolicy) (migrationtestbench.ResourceContract, error) {
	return currentResolver().ResourceContract(scope, id, tests, policy)
}
func IsSourcePolicySchema(value string) bool { return currentResolver().IsSourcePolicySchema(value) }
func IsSchedulingPolicySchema(value string) bool {
	return currentResolver().IsSchedulingPolicySchema(value)
}
