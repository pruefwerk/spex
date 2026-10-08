package definition

import "errors"

type SourceEntry struct {
	Repository string   `json:"repository"`
	Workflow   string   `json:"workflow"`
	Branch     string   `json:"branch"`
	Actors     []string `json:"actors"`
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
	if !r.IsSourcePolicySchema(policy.Schema) || policy.Sources == nil {
		return policy, errors.New("invalid source policy")
	}
	for _, entry := range policy.Sources {
		if entry.Repository == "" || entry.Workflow == "" || entry.Branch == "" || len(entry.Actors) == 0 {
			return policy, errors.New("invalid source admission entry")
		}
		for _, actor := range entry.Actors {
			if actor == "" {
				return policy, errors.New("invalid source actor")
			}
		}
	}
	return policy, nil
}
