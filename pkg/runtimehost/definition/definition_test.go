package definition

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func minimal() Definition {
	d := Defaults()
	d.Suite = "suite-kind-example.yaml"
	d.Binding = "bindings/local.yaml"
	d.Profile = "integration/local.yaml"
	d.Topology = "integration/node.yaml"
	d.Namespace = "acceptance"
	return d
}
func TestDefinitionDefaultsContainNoProjectPolicy(t *testing.T) {
	d := minimal()
	if d.Validate() != nil {
		t.Fatal("minimal policy rejected")
	}
	if len(d.Images) != 0 || len(d.Tags.Profiles) != 0 || d.Credentials.Role != "" || len(d.HealthResources) != 0 {
		t.Fatal("project policy leaked into defaults")
	}
}

func TestBindCopiesNestedPolicyAndIdentityOrdersKeys(t *testing.T) {
	d := minimal()
	d.Tags = Tags{Prefix: "setup-", Defaults: map[string]string{"A": "one", "B": "two"}, Profiles: map[string]map[string]string{"base": {"A": "three"}}}
	first := Identity(d)
	copy := minimal()
	copy.Tags = Tags{Prefix: "setup-", Defaults: map[string]string{"B": "two", "A": "one"}, Profiles: map[string]map[string]string{"base": {"A": "three"}}}
	if Identity(copy) != first {
		t.Fatal("map insertion order changed policy identity")
	}
	Bind(d)
	d.Tags.Profiles["base"]["A"] = "changed"
	if Current().Tags.Profiles["base"]["A"] != "three" {
		t.Fatal("caller mutation changed active policy")
	}
	view := Current()
	view.Tags.Profiles["base"]["A"] = "mutated-view"
	view.Tags.Defaults["A"] = "mutated-view"
	if Current().Tags.Profiles["base"]["A"] != "three" || Current().Tags.Defaults["A"] != "one" {
		t.Fatal("returned view changed active policy")
	}
	if Identity(d) == first {
		t.Fatal("policy change retained identity")
	}
}

func TestOverridesCannotIntroduceUndeclaredSetupKeys(t *testing.T) {
	d := minimal()
	d.Tags = Tags{Prefix: "setup-", Defaults: map[string]string{"KNOWN": "one"}, Profiles: map[string]map[string]string{"base": {"TYPO": "two"}}}
	if d.Validate() == nil {
		t.Fatal("misspelled profile key accepted")
	}
	d.Tags.Profiles["base"] = map[string]string{}
	d.Tags.Overrides = []TagOverride{{Tag: "different", Values: map[string]string{"TYPO": "two"}}}
	if d.Validate() == nil {
		t.Fatal("misspelled override key accepted")
	}
}
func TestDefinitionStrictParsingAndConfinement(t *testing.T) {
	d := minimal()
	data, err := toml.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	file := filepath.Join(root, "host.toml")
	for _, content := range []string{string(data), string(data) + "\nprofiel='wrong'\n", "schema='other'\n", "suite='../../outside'\n"} {
		if os.WriteFile(file, []byte(content), 0600) != nil {
			t.Fatal("fixture")
		}
		_, err := Load(file)
		if (err == nil) != (content == string(data)) {
			t.Fatal("strict parsing failed", err)
		}
	}
}
func TestTypedSetupProfilesAndOverrides(t *testing.T) {
	d := minimal()
	d.Tags = Tags{Prefix: "setup-", Reserved: []string{"setup-", "value-"}, Defaults: map[string]string{"FEATURE": "true", "WINDOW": "1m"},
		Profiles:  map[string]map[string]string{"smoke": {}, "extended": {"WINDOW": "2m"}},
		Overrides: []TagOverride{{Tag: "value-off", Values: map[string]string{"FEATURE": "false"}}, {Tag: "value-short", Exclusive: "window", Values: map[string]string{"WINDOW": "30s"}}, {Tag: "value-long", Exclusive: "window", Profiles: []string{"extended"}, Values: map[string]string{"WINDOW": "5m"}}}}
	if d.Validate() != nil {
		t.Fatal("policy")
	}
	Bind(d)
	value, err := Setup([]string{"setup-smoke", "value-off"})
	if err != nil || !reflect.DeepEqual(value.Environment(), map[string]string{"FEATURE": "false", "WINDOW": "1m"}) {
		t.Fatal(value, err)
	}
	for _, tags := range [][]string{{}, {"setup-smoke", "setup-extended"}, {"setup-unknown"}, {"setup-smoke", "value-unknown"}, {"setup-smoke", "value-long"}, {"setup-extended", "value-short", "value-long"}} {
		if _, err := Setup(tags); err == nil {
			t.Fatal("invalid selection accepted", tags)
		}
	}
	if d.Tags.Defaults["FEATURE"] != "true" {
		t.Fatal("overlay mutated defaults")
	}
}
func TestScopePrefixDoesNotChangeIdentityAlgorithm(t *testing.T) {
	d := minimal()
	Bind(d)
	scope, err := Allocate("owner/repo", 1, 1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "kind")
	if err != nil {
		t.Fatal(err)
	}
	scope.Nonce = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	first, err := scope.Name()
	if err != nil {
		t.Fatal(err)
	}
	d.ScopePrefix = "custom"
	Bind(d)
	secondScope, err := Allocate("owner/repo", 1, 1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "kind")
	if err != nil {
		t.Fatal(err)
	}
	secondScope.Nonce = scope.Nonce
	second, err := secondScope.Name()
	if unchanged, _ := scope.Name(); unchanged != first {
		t.Fatal("rebinding renamed an existing scope")
	}
	if err != nil || first[len("spex-"):] != second[len("custom-"):] {
		t.Fatal("identity hash changed", first, second, err)
	}
}
func TestInvalidPolicyIsRejectedBeforeBinding(t *testing.T) {
	for _, mutate := range []func(*Definition){
		func(d *Definition) { d.ScopeEnvironment = "bad-name" },
		func(d *Definition) { d.Groups = []string{"baseline", "baseline"} },
		func(d *Definition) { d.Topology = "/outside" },
		func(d *Definition) { d.CleanupTimeout = "0s" },
		func(d *Definition) { d.Images = []Image{{Alias: "a"}} },
		func(d *Definition) { d.ResourceTypes = map[string]string{"target": "arbitrary-eval"} },
	} {
		d := minimal()
		mutate(&d)
		if d.Validate() == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}
