package workspace

import "testing"

func TestCatalogOverlayPreservesInheritedDefinitions(t *testing.T) {
	base := CatalogBundle{Flows: map[string]FlowDefinition{"shared": {}, "replace": {}}, Steps: []StepDefinition{{Kind: "given", Expression: "shared"}, {Kind: "when", Expression: "replace"}}}
	override := CatalogBundle{Flows: map[string]FlowDefinition{"replace": {Parameters: map[string]string{"source": "caller"}}}, Steps: []StepDefinition{{Kind: "when", Expression: "replace", Output: CatalogExpansion{Parameters: map[string]Parameter{"source": {Default: "caller"}}}}}}
	result := OverlayCatalogBundle(base, override)
	if len(result.Flows) != 2 || result.Flows["replace"].Parameters["source"] != "caller" || len(result.Steps) != 2 || result.Steps[1].Output.Parameters["source"].Default != "caller" {
		t.Fatal("caller precedence or inheritance lost")
	}
	if base.Flows["replace"].Parameters != nil || base.Steps[1].Output.Parameters != nil {
		t.Fatal("overlay mutated inherited catalog")
	}
}
