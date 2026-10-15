package definition

import "testing"

func TestSuiteResourcesReplaceOnlySelectedInventories(t *testing.T) {
	d := minimal()
	d.Images = []Image{{ProbeAlias: true, Build: Build{Context: ".", Dockerfile: "probe/Dockerfile", Arguments: map[string]string{"BASE": "original"}}}, {Alias: "default-stub:test", Build: Build{Context: "stub", Dockerfile: "stub/Dockerfile"}}}
	d.ImageInputs = []string{"probe"}
	d.ImageServices = []string{"default-service"}
	d.HealthResources = []string{"default-workload"}
	d.Suites = []SuitePolicy{{Suite: "another.yaml", IncludeImages: []string{"probe"}, ExtraImages: []Image{{Alias: "other-stub:test", Build: Build{Context: "other", Dockerfile: "other/Dockerfile"}}}}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	selected, err := d.SuiteResources("another.yaml")
	if err != nil || len(selected.Images) != 2 || selected.Images[1].Alias != "other-stub:test" || len(selected.ImageServices) != 0 || len(selected.HealthResources) != 0 || len(selected.ImageInputs) != 3 {
		t.Fatal("suite inventories did not replace default inventories", err)
	}
	if selected.SchedulingPolicy != d.SchedulingPolicy || selected.SourcePolicy != d.SourcePolicy || selected.Credentials != d.Credentials {
		t.Fatal("suite changed admission, scheduling or credentials")
	}
	selected.Images[0].Build.Arguments["BASE"] = "changed"
	if d.Images[0].Build.Arguments["BASE"] != "original" {
		t.Fatal("suite selection mutated defaults")
	}
	d.Suites[0].ExtraImages[0].Build.Contexts = map[string]string{"second": "second-input", "first": "first-input"}
	selected, err = d.SuiteResources("another.yaml")
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity(selected)
	for range 20 {
		selected, err := d.SuiteResources("another.yaml")
		if err != nil || Identity(selected) != identity {
			t.Fatal("map iteration changed suite policy identity", err)
		}
	}
	for _, policy := range []SuitePolicy{
		{Suite: "../outside.yaml"}, {Suite: d.Suite},
		{Suite: "another.yaml", IncludeImages: []string{"unknown"}},
		{Suite: "another.yaml", IncludeImages: []string{"probe", "probe"}},
		{Suite: "another.yaml", ExtraImages: []Image{{Alias: "invalid"}}},
	} {
		invalid := Clone(d)
		invalid.Suites = []SuitePolicy{policy}
		if invalid.Validate() == nil {
			t.Fatal("invalid suite policy accepted", policy.Suite)
		}
	}
	d.Suites = append(d.Suites, d.Suites[0])
	if d.Validate() == nil {
		t.Fatal("duplicate suite registration accepted")
	}
}
