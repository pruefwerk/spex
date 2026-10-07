// Package scenario defines portable acceptance descriptions. It does not resolve
// runtime defaults, expand secrets, or interpret test-language semantics.
package scenario

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const Schema = "spex.scenario/v1"
const RequestSchema = "spex.scenario-request/v1"
const MaxDocumentBytes = 4 << 20

type Metadata struct {
	Name        string  `toml:"name,omitempty"`
	Description string  `toml:"description,omitempty"`
	Timeout     *string `toml:"timeout,omitempty"`
}

type TestSource struct {
	Name   string  `toml:"name,omitempty"`
	File   *string `toml:"file,omitempty"`
	Inline *string `toml:"inline,omitempty"`
}

type Scenario struct {
	Schema         string
	Runtime        string
	RuntimeRelease string
	Metadata       Metadata
	Tests          []TestSource
	Dependencies   []string
	RuntimeConfig  RawRuntimeConfig
	RuntimeOverlay RawRuntimeConfig
}

// RawRuntimeConfig owns canonical TOML bytes. Accessors copy storage so callers
// cannot mutate another scenario's configuration through a shared map or slice.
type RawRuntimeConfig struct{ document string }

func (r RawRuntimeConfig) Bytes() []byte { return []byte(r.document) }
func (r RawRuntimeConfig) Empty() bool   { return r.document == "" }

func ParseRuntimeConfig(data []byte) (RawRuntimeConfig, error) {
	if len(data) > MaxDocumentBytes {
		return RawRuntimeConfig{}, errors.New("runtime configuration exceeds size limit")
	}
	var values map[string]any
	if err := toml.Unmarshal(data, &values); err != nil {
		return RawRuntimeConfig{}, errors.New("invalid runtime configuration TOML")
	}
	encoded, err := toml.Marshal(values)
	if err != nil {
		return RawRuntimeConfig{}, errors.New("cannot encode runtime configuration")
	}
	return RawRuntimeConfig{document: string(encoded)}, nil
}

// DecodeStrict leaves field interpretation to the selected runtime. Errors omit
// input excerpts, which may contain credentials.
func (r RawRuntimeConfig) DecodeStrict(destination any) error {
	if err := toml.NewDecoder(bytes.NewReader(r.Bytes())).DisallowUnknownFields().Decode(destination); err != nil {
		return errors.New("invalid runtime configuration: unknown field or invalid value")
	}
	return nil
}

type document struct {
	Schema         string         `toml:"schema"`
	Runtime        string         `toml:"runtime"`
	RuntimeRelease string         `toml:"runtime_release,omitempty"`
	Metadata       *Metadata      `toml:"scenario,omitempty"`
	Tests          []TestSource   `toml:"tests,omitempty"`
	Dependencies   []string       `toml:"dependencies,omitempty"`
	RuntimeConfig  map[string]any `toml:"runtime_config,omitempty"`
	RuntimeOverlay map[string]any `toml:"runtime_overlay,omitempty"`
}

func Parse(data []byte) (Scenario, error) {
	return parse(data, false)
}

// ParseRequest accepts authoring requests as well as complete scenarios.
// A request must be resolved by a receiver before it can execute.
func ParseRequest(data []byte) (Scenario, error) { return parse(data, true) }

func parse(data []byte, request bool) (Scenario, error) {
	if len(data) > MaxDocumentBytes {
		return Scenario{}, errors.New("scenario exceeds size limit")
	}
	var wire document
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&wire); err != nil {
		return Scenario{}, errors.New("invalid scenario TOML: syntax, unknown field, or invalid field type")
	}
	raw, err := toml.Marshal(wire.RuntimeConfig)
	if err != nil {
		return Scenario{}, errors.New("invalid runtime configuration")
	}
	config, err := ParseRuntimeConfig(raw)
	if err != nil {
		return Scenario{}, err
	}
	overlayBytes, err := toml.Marshal(wire.RuntimeOverlay)
	if err != nil {
		return Scenario{}, errors.New("invalid runtime overlay")
	}
	overlay, err := ParseRuntimeConfig(overlayBytes)
	if err != nil {
		return Scenario{}, err
	}
	s := Scenario{Schema: wire.Schema, Runtime: wire.Runtime, RuntimeRelease: wire.RuntimeRelease, Tests: wire.Tests, Dependencies: wire.Dependencies, RuntimeConfig: config, RuntimeOverlay: overlay}
	if wire.Metadata != nil {
		s.Metadata = *wire.Metadata
	}
	return canonicalize(s, request)
}

var runtimePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*/v[1-9][0-9]*$`)

func RuntimeID(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func Canonicalize(s Scenario) (Scenario, error) {
	return canonicalize(s, false)
}

func CanonicalizeRequest(s Scenario) (Scenario, error) { return canonicalize(s, true) }

func canonicalize(s Scenario, request bool) (Scenario, error) {
	out := s
	out.Schema = strings.ToLower(strings.TrimSpace(s.Schema))
	out.Runtime = RuntimeID(s.Runtime)
	draft := request && out.Schema == RequestSchema
	if out.Schema != Schema && !draft {
		return Scenario{}, errors.New("unsupported scenario schema")
	}
	if draft {
		selector, err := ParseRuntimeSelector(s.Runtime)
		if err != nil {
			return Scenario{}, err
		}
		out.Runtime = selector.String()
	} else if !runtimePattern.MatchString(out.Runtime) {
		return Scenario{}, errors.New("invalid or missing runtime identifier")
	}
	if !draft && !out.RuntimeOverlay.Empty() {
		return Scenario{}, errors.New("unresolved runtime overlay")
	}
	if out.RuntimeRelease != "" && (draft || !releasePattern.MatchString(out.RuntimeRelease) || out.RuntimeRelease == "latest") {
		return Scenario{}, errors.New("invalid resolved runtime release")
	}
	if s.Metadata.Timeout != nil {
		duration, err := time.ParseDuration(*s.Metadata.Timeout)
		if err != nil || duration <= 0 {
			return Scenario{}, errors.New("scenario timeout must be a positive duration")
		}
		value := duration.String()
		out.Metadata.Timeout = &value
	}
	out.Tests = make([]TestSource, len(s.Tests))
	out.Dependencies = make([]string, 0, len(s.Dependencies))
	seen := map[string]bool{}
	for _, dependency := range s.Dependencies {
		path, err := RelativePath(dependency)
		if err != nil {
			return Scenario{}, errors.New("invalid dependency path")
		}
		if !seen[path] {
			out.Dependencies = append(out.Dependencies, path)
			seen[path] = true
		}
	}
	sort.Strings(out.Dependencies)
	for i, test := range s.Tests {
		if (test.File == nil) == (test.Inline == nil) {
			return Scenario{}, fmt.Errorf("test[%d] requires exactly one of file and inline", i)
		}
		out.Tests[i] = TestSource{Name: test.Name}
		if test.File != nil {
			value, err := RelativePath(*test.File)
			if err != nil {
				return Scenario{}, fmt.Errorf("test[%d]: invalid workspace-relative file path", i)
			}
			out.Tests[i].File = &value
		} else {
			value := strings.ReplaceAll(strings.ReplaceAll(*test.Inline, "\r\n", "\n"), "\r", "\n")
			if strings.TrimSpace(value) == "" {
				return Scenario{}, fmt.Errorf("test[%d]: inline source is empty", i)
			}
			out.Tests[i].Inline = &value
		}
	}
	return out, nil
}

func ValidateGeneric(s Scenario) error { _, err := Canonicalize(s); return err }

func RelativePath(value string) (string, error) {
	value = strings.ReplaceAll(value, "\\", "/")
	if strings.ContainsRune(value, 0) || strings.Contains(value, ":") || strings.HasPrefix(value, "/") {
		return "", errors.New("path must remain within workspace")
	}
	value = path.Clean(value)
	if value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return "", errors.New("path must remain within workspace")
	}
	return value, nil
}

// SourcePath also checks symlinks. All external source paths are relative to the
// explicitly selected workspace, independent of the scenario file's location.
func SourcePath(workspace, relative string) (string, error) {
	clean, err := RelativePath(relative)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", errors.New("workspace unavailable")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", errors.New("workspace unavailable")
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(clean)))
	if err != nil {
		return "", errors.New("referenced source is missing or inaccessible")
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("source escapes workspace")
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("referenced source must be a regular file")
	}
	return target, nil
}

func ValidateSources(s Scenario, workspace string) error {
	if _, err := CanonicalizeRequest(s); err != nil {
		return err
	}
	for i, source := range s.Tests {
		if source.File != nil {
			if _, err := SourcePath(workspace, *source.File); err != nil {
				return fmt.Errorf("test[%d]: %w", i, err)
			}
		}
	}
	for _, dependency := range s.Dependencies {
		if _, err := SourcePath(workspace, dependency); err != nil {
			return errors.New("dependency unavailable or outside workspace")
		}
	}
	return nil
}

func SerializeCanonical(s Scenario) ([]byte, error) {
	return serialize(s, false)
}

func SerializeRequest(s Scenario) ([]byte, error) { return serialize(s, true) }

func serialize(s Scenario, request bool) ([]byte, error) {
	s, err := canonicalize(s, request)
	if err != nil {
		return nil, err
	}
	var config map[string]any
	if err := toml.Unmarshal(s.RuntimeConfig.Bytes(), &config); err != nil {
		return nil, errors.New("invalid runtime configuration")
	}
	var overlay map[string]any
	if err := toml.Unmarshal(s.RuntimeOverlay.Bytes(), &overlay); err != nil {
		return nil, errors.New("invalid runtime overlay")
	}
	wire := document{Schema: s.Schema, Runtime: s.Runtime, RuntimeRelease: s.RuntimeRelease, Tests: s.Tests, Dependencies: s.Dependencies, RuntimeConfig: config, RuntimeOverlay: overlay}
	if s.Metadata.Name != "" || s.Metadata.Description != "" || s.Metadata.Timeout != nil {
		wire.Metadata = &s.Metadata
	}
	data, err := toml.Marshal(wire)
	if err != nil {
		return nil, errors.New("cannot serialize scenario")
	}
	return data, nil
}

func Identity(s Scenario) (string, error) {
	data, err := SerializeCanonical(s)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// AuthoringOverrides preserves absent versus explicitly empty metadata. Runtime
// configuration merging belongs to the runtime's typed overlay implementation.
type AuthoringOverrides struct {
	Runtime       *string
	Name          *string
	Description   *string
	Timeout       *string
	Tests         []TestSource
	RuntimeConfig *RawRuntimeConfig
}

type ConfigMerger func(base, overlay RawRuntimeConfig) (RawRuntimeConfig, error)

func Build(runtime string, overrides AuthoringOverrides, merge ConfigMerger) (Scenario, error) {
	return MergeAuthoringOverrides(Scenario{Schema: Schema, Runtime: runtime}, overrides, false, merge)
}

func MergeAuthoringOverrides(base Scenario, o AuthoringOverrides, committed bool, merge ConfigMerger) (Scenario, error) {
	return mergeAuthoring(base, o, committed, merge, false)
}

// AuthorRequest retains both configuration layers until a receiver selects the
// runtime and applies its typed merger. No caller-side runtime is required.
func AuthorRequest(base Scenario, o AuthoringOverrides, committed bool) (Scenario, error) {
	base.Schema = RequestSchema
	if base.RuntimeRelease != "" {
		base.Runtime += "@" + base.RuntimeRelease
		base.RuntimeRelease = ""
	}
	if o.RuntimeConfig != nil {
		if !base.RuntimeOverlay.Empty() {
			return Scenario{}, errors.New("request already has a final runtime overlay")
		}
		base.RuntimeOverlay = *o.RuntimeConfig
		o.RuntimeConfig = nil
	}
	return mergeAuthoring(base, o, committed, nil, true)
}

func mergeAuthoring(base Scenario, o AuthoringOverrides, committed bool, merge ConfigMerger, request bool) (Scenario, error) {
	if committed && o.Tests != nil {
		return Scenario{}, errors.New("scenario file cannot be combined with author-provided test sources")
	}
	if o.Runtime != nil {
		mismatch := RuntimeID(*o.Runtime) != RuntimeID(base.Runtime)
		if request {
			before, err := ParseRuntimeSelector(base.Runtime)
			if err != nil {
				return Scenario{}, err
			}
			after, err := ParseRuntimeSelector(*o.Runtime)
			if err != nil {
				return Scenario{}, err
			}
			mismatch = before.Contract != "" && (after.Contract != before.Contract || (before.Release != "" && before.Release != "latest" && after.Release != before.Release))
		}
		if committed && mismatch {
			return Scenario{}, errors.New("scenario runtime mismatch")
		}
		base.Runtime = *o.Runtime
	}
	if o.Name != nil {
		base.Metadata.Name = *o.Name
	}
	if o.Description != nil {
		base.Metadata.Description = *o.Description
	}
	if o.Timeout != nil {
		base.Metadata.Timeout = o.Timeout
	}
	if o.Tests != nil {
		base.Tests = o.Tests
	}
	if o.RuntimeConfig != nil {
		if merge == nil {
			return Scenario{}, errors.New("runtime configuration merger unavailable")
		}
		config, err := merge(base.RuntimeConfig, *o.RuntimeConfig)
		if err != nil {
			return Scenario{}, err
		}
		base.RuntimeConfig = config
	}
	return canonicalize(base, request)
}
