package definition

import (
	"errors"
	"regexp"
)

type SourceEntry struct {
	Repository   string   `json:"repository,omitempty"`
	Organization string   `json:"organization,omitempty"`
	Visibilities []string `json:"visibilities,omitempty"`
	Workflow     string   `json:"workflow,omitempty"`
	Branch       string   `json:"branch,omitempty"`
	Actors       []string `json:"actors,omitempty"`
}
type SourcePolicy struct {
	Schema  string        `json:"schema"`
	Sources []SourceEntry `json:"sources"`
}

func (r *Resolver) ReadSourcePolicy(path string) (SourcePolicy, error) {
	var policy SourcePolicy
	if err := readDocument(path, &policy, true); err != nil {
		return policy, err
	}
	if !r.IsSourcePolicySchema(policy.Schema) || policy.Validate() != nil {
		return policy, errors.New("invalid source policy")
	}
	return policy, nil
}

// Validate keeps selectors explicit. Organization rules require non-public
// visibility; repository rules also support the existing exact-repository policy.
func (policy SourcePolicy) Validate() error {
	if policy.Sources == nil {
		return errors.New("invalid source policy")
	}
	owner := regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repository := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]+$`)
	for _, entry := range policy.Sources {
		if (entry.Repository == "") == (entry.Organization == "") ||
			(entry.Repository != "" && !repository.MatchString(entry.Repository)) ||
			(entry.Organization != "" && (!owner.MatchString(entry.Organization) || len(entry.Visibilities) == 0)) ||
			(entry.Visibilities != nil && len(entry.Visibilities) == 0) ||
			(entry.Actors != nil && len(entry.Actors) == 0) {
			return errors.New("invalid source admission entry")
		}
		for _, visibility := range entry.Visibilities {
			if visibility != "internal" && visibility != "private" {
				return errors.New("invalid source visibility")
			}
		}
		for _, actor := range entry.Actors {
			if actor == "" {
				return errors.New("invalid source actor")
			}
		}
	}
	return nil
}
