package scenario

import (
	"errors"
	"regexp"
	"strings"
)

var releasePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

type RuntimeSelector struct{ Contract, Release string }

func (s RuntimeSelector) String() string {
	if s.Contract == "" {
		return ""
	}
	if s.Release == "" {
		return s.Contract
	}
	return s.Contract + "@" + s.Release
}

// Empty/latest selects the receiver default. A contract without a release
// selects that contract's configured default, never an inferred version order.
func ParseRuntimeSelector(value string) (RuntimeSelector, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "latest" {
		return RuntimeSelector{}, nil
	}
	contract, release, qualified := strings.Cut(value, "@")
	contract = RuntimeID(contract)
	if !runtimePattern.MatchString(contract) || (qualified && !releasePattern.MatchString(release)) {
		return RuntimeSelector{}, errors.New("runtime must be contract/vN or contract/vN@release")
	}
	return RuntimeSelector{contract, release}, nil
}

// RuntimeRelease entries come from trusted receiver configuration, not requests.
// Release names must identify immutable implementations in the receiver catalog.
type RuntimeRelease struct {
	Contract string
	Release  string
	Default  bool
	Merge    ConfigMerger
}

func ResolveRequest(request Scenario, releases []RuntimeRelease, defaultContract string) (Scenario, error) {
	request, err := CanonicalizeRequest(request)
	if err != nil {
		return Scenario{}, err
	}
	selector, err := ParseRuntimeSelector(request.Runtime)
	if err != nil {
		return Scenario{}, err
	}
	if request.RuntimeRelease != "" {
		selector.Release = request.RuntimeRelease
	}
	if selector.Contract == "" {
		selector.Contract = defaultContract
	}
	var selected *RuntimeRelease
	for i := range releases {
		entry := &releases[i]
		if !runtimePattern.MatchString(entry.Contract) || !releasePattern.MatchString(entry.Release) || entry.Release == "latest" {
			return Scenario{}, errors.New("invalid receiver runtime catalog")
		}
		if entry.Contract != selector.Contract {
			continue
		}
		matches := entry.Release == selector.Release
		if selector.Release == "" || selector.Release == "latest" {
			matches = entry.Default
		}
		if matches {
			if selected != nil {
				return Scenario{}, errors.New("ambiguous receiver runtime selection")
			}
			selected = entry
		}
	}
	if selected == nil {
		return Scenario{}, errors.New("requested runtime is not supported by receiver")
	}
	if selected.Merge == nil {
		return Scenario{}, errors.New("receiver runtime merger unavailable")
	}
	config, err := selected.Merge(request.RuntimeConfig, request.RuntimeOverlay)
	if err != nil {
		return Scenario{}, errors.New("selected runtime rejected configuration")
	}
	request.Schema = Schema
	request.Runtime = selected.Contract
	request.RuntimeRelease = selected.Release
	request.RuntimeConfig = config
	request.RuntimeOverlay = RawRuntimeConfig{}
	return Canonicalize(request)
}
