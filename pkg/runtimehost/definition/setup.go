package definition

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
)

type SetupValues struct{ Values map[string]string }

func (s SetupValues) Environment() map[string]string {
	result := map[string]string{}
	for key, value := range s.Values {
		result[key] = value
	}
	return result
}
func (r *Resolver) Setup(tags []string) (SetupValues, error) {
	policy := r.snapshot.Tags
	selected := map[string]bool{}
	for _, tag := range tags {
		selected[tag] = true
	}
	profile, count := "", 0
	for tag := range selected {
		if strings.HasPrefix(tag, policy.Prefix) {
			profile = strings.TrimPrefix(tag, policy.Prefix)
			count++
		}
	}
	values, ok := policy.Profiles[profile]
	if count != 1 || !ok || policy.Prefix == "" {
		return SetupValues{}, errors.New("declare exactly one known setup profile")
	}
	result := SetupValues{Values: map[string]string{}}
	for key, value := range policy.Defaults {
		result.Values[key] = value
	}
	for key, value := range values {
		result.Values[key] = value
	}
	known := map[string]TagOverride{}
	for _, override := range policy.Overrides {
		known[override.Tag] = override
	}
	unknown := []string{}
	exclusive := map[string]bool{}
	for tag := range selected {
		for _, prefix := range policy.Reserved {
			if strings.HasPrefix(tag, prefix) && tag != policy.Prefix+profile {
				if _, ok := known[tag]; !ok {
					unknown = append(unknown, tag)
				}
				break
			}
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return SetupValues{}, errors.New("unknown setup tags: " + strings.Join(unknown, ", "))
	}
	for _, override := range policy.Overrides {
		if !selected[override.Tag] {
			continue
		}
		allowed := len(override.Profiles) == 0
		for _, name := range override.Profiles {
			if name == profile {
				allowed = true
			}
		}
		if !allowed {
			return SetupValues{}, errors.New("setup override is not allowed for this profile")
		}
		if override.Exclusive != "" {
			if exclusive[override.Exclusive] {
				return SetupValues{}, errors.New("declare only one override for each exclusive setting")
			}
			exclusive[override.Exclusive] = true
		}
		for key, value := range override.Values {
			result.Values[key] = value
		}
	}
	return result, nil
}
func (r *Resolver) ReadSetup(workspace string) (SetupValues, error) {
	var value struct {
		Version string   `json:"apiVersion"`
		Tags    []string `json:"tags"`
	}
	if readDocument(filepath.Join(workspace, "scenario-context.json"), &value, false) != nil || value.Version != "spex.context.v0.1" {
		return SetupValues{}, errors.New("generated scenario context missing or invalid; recompile with Spex")
	}
	return r.Setup(value.Tags)
}
