package definition

// Spex owns policy formats. Runtime repositories supply their policy values.
const SourcePolicySchema = "spex.receiver-source-policy/v1"
const SchedulingPolicySchema = "spex.scheduling-policy/v1"

type SchedulingPolicy struct {
	Schema           string  `json:"schema"`
	ConnectionString *string `json:"connection_string,omitempty"`
	Database         string  `json:"database"`
	Collection       string  `json:"collection"`
	Pool             string  `json:"pool"`
	Capacity         int     `json:"capacity"`
}

// Earlier hosts used their ledger prefix for policy format names too. Accept
// those identifiers during transition without making the new schemas host-owned.
func (r *Resolver) IsSourcePolicySchema(value string) bool {
	return value == SourcePolicySchema || value == r.Protocol("receiver-source-policy")
}

func (r *Resolver) IsSchedulingPolicySchema(value string) bool {
	return value == SchedulingPolicySchema || value == r.Protocol("scheduling-policy")
}
