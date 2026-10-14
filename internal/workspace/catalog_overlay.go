package workspace

// OverlayCatalogBundle gives the later layer precedence for equal flow names
// and equal step kind/expression pairs. Unmentioned definitions remain inherited.
// Each layer must first pass LoadCatalogBundle's validation independently.
func OverlayCatalogBundle(base, override CatalogBundle) CatalogBundle {
	result := CatalogBundle{Flows: map[string]FlowDefinition{}}
	for name, flow := range base.Flows {
		result.Flows[name] = flow
	}
	for name, flow := range override.Flows {
		result.Flows[name] = flow
	}
	key := func(step StepDefinition) string { return step.Kind + "\x00" + step.Expression }
	replaced := map[string]bool{}
	for _, step := range override.Steps {
		replaced[key(step)] = true
	}
	for _, step := range base.Steps {
		if !replaced[key(step)] {
			result.Steps = append(result.Steps, step)
		}
	}
	result.Steps = append(result.Steps, override.Steps...)
	for _, flow := range base.Inventory.Flows {
		if _, ok := override.Flows[flow.Name]; !ok {
			result.Inventory.Flows = append(result.Inventory.Flows, flow)
		}
	}
	result.Inventory.Flows = append(result.Inventory.Flows, override.Inventory.Flows...)
	for _, step := range base.Inventory.Steps {
		if !replaced[key(step.Step)] {
			result.Inventory.Steps = append(result.Inventory.Steps, step)
		}
	}
	result.Inventory.Steps = append(result.Inventory.Steps, override.Inventory.Steps...)
	return result
}
