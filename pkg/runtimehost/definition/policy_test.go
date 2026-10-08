package definition

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPolicySchemasBelongToSpex(t *testing.T) {
	d := minimal()
	d.SchemaPrefix = "mtb"
	Bind(d)
	for _, schema := range []string{SourcePolicySchema, "mtb.receiver-source-policy/v1"} {
		if !IsSourcePolicySchema(schema) {
			t.Fatal("source schema rejected", schema)
		}
		path := filepath.Join(t.TempDir(), "source.json")
		if err := os.WriteFile(path, []byte(`{"schema":"`+schema+`","sources":[]}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSourcePolicy(path); err != nil {
			t.Fatal(err)
		}
	}
	for _, schema := range []string{SchedulingPolicySchema, "mtb.scheduling-policy/v1"} {
		if !IsSchedulingPolicySchema(schema) {
			t.Fatal("scheduling schema rejected", schema)
		}
	}
	for _, schema := range []string{"spex.scheduling-policy/v2", "other.scheduling-policy/v1", SourcePolicySchema} {
		if IsSchedulingPolicySchema(schema) {
			t.Fatal("unsupported scheduling schema accepted", schema)
		}
	}
	if IsSourcePolicySchema(SchedulingPolicySchema) {
		t.Fatal("wrong policy kind accepted")
	}
}
