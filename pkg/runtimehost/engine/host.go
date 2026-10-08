package engine

import (
	"os"
	"path/filepath"
	"regexp"

	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
)

// Host owns one immutable definition and release identity. It never changes the
// compatibility definition binding used by SDK clients.
type Host struct {
	config   hostconfig.Definition
	resolver *hostconfig.Resolver
	path     string
	version  string
}

// New validates and snapshots a trusted definition without changing the
// environment. Later operations keep this policy and version until a new host
// is constructed; prepared-execution checks still detect policy-file drift.
func New(path, version string) (*Host, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	d, err := hostconfig.Load(absolute)
	if err != nil {
		return nil, err
	}
	resolver, err := hostconfig.NewResolver(d)
	if err != nil {
		return nil, err
	}
	return &Host{config: resolver.Snapshot(), resolver: resolver, path: absolute, version: version}, nil
}

func pathFromArgs(args []string) string {
	if len(args) >= 2 && args[0] == "--config" {
		return args[1]
	}
	if path := os.Getenv(hostconfig.ConfigEnvironment); path != "" {
		return path
	}
	return "spex-runtime.toml"
}

func (h *Host) scopePattern() string {
	return "^" + regexp.QuoteMeta(h.config.ScopePrefix) + "-[a-f0-9]{32}$"
}

// The inherited suite resolver still reads process environment. Retain the
// command boundary's serialization and restore every host-owned key afterward.
func (h *Host) captureEnvironment() func() {
	type value struct {
		key, text string
		present   bool
	}
	values := []value{}
	for _, key := range []string{hostconfig.ConfigEnvironment, h.config.ScopeEnvironment, "KUBECONFIG", "SPEX_SUITE"} {
		text, present := os.LookupEnv(key)
		values = append(values, value{key, text, present})
	}
	return func() {
		for _, v := range values {
			if v.present {
				_ = os.Setenv(v.key, v.text)
			} else {
				_ = os.Unsetenv(v.key)
			}
		}
	}
}
