package definition

import (
	"errors"
	"sort"
)

// SuiteResources applies only typed, runtime-owned resource selections. Arrays
// replace the default suite's inventory; scheduling and admission policy inherit.
func (d Definition) SuiteResources(suite string) (Definition, error) {
	d = Clone(d)
	for _, policy := range d.Suites {
		if policy.Suite != suite {
			continue
		}
		selected := Clone(d)
		selected.Suites = nil
		selected.Suite = suite
		selected.Images = nil
		seen := map[string]bool{}
		for _, alias := range policy.IncludeImages {
			if seen[alias] {
				return Definition{}, errors.New("duplicate suite image selection")
			}
			seen[alias] = true
			found := false
			for _, image := range d.Images {
				if (alias == "probe" && image.ProbeAlias) || (!image.ProbeAlias && alias == image.Alias) {
					selected.Images = append(selected.Images, image)
					found = true
				}
			}
			if !found {
				return Definition{}, errors.New("suite image selection unavailable")
			}
		}
		extra := Clone(Definition{Images: policy.ExtraImages}).Images
		selected.Images = append(selected.Images, extra...)
		for _, image := range extra {
			selected.ImageInputs = append(selected.ImageInputs, image.Build.Context, image.Build.Dockerfile)
			inputs := make([]string, 0, len(image.Build.Contexts))
			for _, input := range image.Build.Contexts {
				inputs = append(inputs, input)
			}
			sort.Strings(inputs)
			selected.ImageInputs = append(selected.ImageInputs, inputs...)
		}
		selected.ImageServices = append([]string(nil), policy.ImageServices...)
		selected.HealthResources = append([]string(nil), policy.HealthResources...)
		if err := selected.Validate(); err != nil {
			return Definition{}, err
		}
		return selected, nil
	}
	return Definition{}, errors.New("suite policy unavailable")
}
