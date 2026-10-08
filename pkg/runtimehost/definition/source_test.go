package definition

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSourcePolicySelectors(t *testing.T) {
	for _, tc := range []struct {
		name, entries string
		valid         bool
	}{
		{"deny all", `[]`, true},
		{"organization", `[{"organization":"acme","visibilities":["internal","private"]}]`, true},
		{"repository", `[{"repository":"acme/service"}]`, true},
		{"legacy", `[{"repository":"acme/service","workflow":".github/workflows/ci.yaml","branch":"main","actors":["bot"]}]`, true},
		{"no selectors", `[{}]`, false},
		{"ambiguous selectors", `[{"repository":"acme/service","organization":"acme","visibilities":["internal"]}]`, false},
		{"unbounded visibility", `[{"organization":"acme"}]`, false},
		{"public organization", `[{"organization":"acme","visibilities":["public"]}]`, false},
		{"unknown visibility", `[{"organization":"acme","visibilities":["internl"]}]`, false},
		{"empty visibility", `[{"organization":"acme","visibilities":[]}]`, false},
		{"empty actors", `[{"repository":"acme/service","actors":[]}]`, false},
		{"empty actor", `[{"repository":"acme/service","actors":[""]}]`, false},
		{"wildcard organization", `[{"organization":"*","visibilities":["internal"]}]`, false},
		{"wildcard repository", `[{"repository":"acme/*"}]`, false},
		{"unknown field", `[{"organization":"acme","visibilities":["internal"],"branches":[]}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "policy.json")
			if err := os.WriteFile(path, []byte(`{"schema":"spex.receiver-source-policy/v1","sources":`+tc.entries+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			resolver, err := NewResolver(minimal())
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolver.ReadSourcePolicy(path)
			if (err == nil) != tc.valid {
				t.Fatalf("error=%v want valid=%v", err, tc.valid)
			}
		})
	}
}
