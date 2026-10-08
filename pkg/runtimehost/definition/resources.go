package definition

import (
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/pruefwerk/spex/pkg/migrationtestbench"
	"github.com/pruefwerk/spex/pkg/resourceclaims"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

type PhysicalResource struct {
	Type   string                `json:"type"`
	ID     string                `json:"id"`
	Access resourceclaims.Access `json:"access"`
}
type PolicyTest struct {
	Name      string             `json:"name"`
	Source    string             `json:"source"`
	Resources []PhysicalResource `json:"resources"`
}
type ResourcePolicy struct {
	Schema      string       `json:"schema"`
	Environment string       `json:"environment"`
	Tests       []PolicyTest `json:"tests"`
}

func ReadResourcePolicy(path string) (ResourcePolicy, error) {
	var policy ResourcePolicy
	err := readDocument(path, &policy, true)
	return policy, err
}

// ResourceContract names physical mutation targets, never evidence IDs or run
// scopes. It does not grant AWS admission or prove application cleanup safety.
func (r *Resolver) ResourceContract(scope Scope, id string, tests []scenarioruntime.TestDescription, policy *ResourcePolicy) (migrationtestbench.ResourceContract, error) {
	fail := errors.New("invalid or incomplete trusted resource contract")
	if _, err := scope.Name(); err != nil {
		return migrationtestbench.ResourceContract{}, fail
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(id) || len(tests) == 0 {
		return migrationtestbench.ResourceContract{}, fail
	}
	for _, test := range tests {
		if test.Name == "" || test.Source == "" {
			return migrationtestbench.ResourceContract{}, fail
		}
	}
	contract := migrationtestbench.ResourceContract{Schema: "mtb.resource-contract/v1", ScenarioID: id, Tests: slices.Clone(tests), Claims: []resourceclaims.Claim{}}
	if scope.Target == "kind" {
		if policy != nil {
			return migrationtestbench.ResourceContract{}, fail
		}
		return contract, nil
	}
	identity := regexp.MustCompile(`^[A-Za-z0-9_.:/@+-]{1,400}$`)
	if scope.Target != "aws" || policy == nil || policy.Schema != r.Protocol("resource-policy") || !identity.MatchString(policy.Environment) || policy.Tests == nil {
		return migrationtestbench.ResourceContract{}, fail
	}
	types := r.snapshot.ResourceTypes
	type key struct{ Name, Source string }
	entries := map[key][]resourceclaims.Claim{}
	for _, entry := range policy.Tests {
		k := key{entry.Name, entry.Source}
		if entry.Name == "" || entry.Source == "" || entry.Resources == nil {
			return migrationtestbench.ResourceContract{}, fail
		}
		if _, exists := entries[k]; exists {
			return migrationtestbench.ResourceContract{}, fail
		}
		claims := []resourceclaims.Claim{}
		for _, resource := range entry.Resources {
			if types[resource.Type] == "" || !identity.MatchString(resource.ID) || (resource.Access != resourceclaims.Shared && resource.Access != resourceclaims.Exclusive) {
				return migrationtestbench.ResourceContract{}, fail
			}
			value := resource.ID
			if types[resource.Type] == "hex16_upper" {
				if !regexp.MustCompile(`^[a-fA-F0-9]{16}$`).MatchString(value) {
					return migrationtestbench.ResourceContract{}, fail
				}
				value = strings.ToUpper(value)
			}
			physical := policy.Environment + "/" + resource.Type + "/" + value
			if len(physical) > 512 {
				return migrationtestbench.ResourceContract{}, fail
			}
			claims = append(claims, resourceclaims.Claim{Resource: physical, Access: resource.Access})
		}
		entries[k] = claims
	}
	for _, test := range tests {
		claims, exists := entries[key{test.Name, test.Source}]
		if !exists {
			return migrationtestbench.ResourceContract{}, fail
		}
		contract.Claims = append(contract.Claims, claims...)
	}
	if len(contract.Claims) > 1000 {
		return migrationtestbench.ResourceContract{}, fail
	}
	return contract, nil
}
