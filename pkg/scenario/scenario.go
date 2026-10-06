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
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const Schema = "spex.scenario/v1"
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
	Schema        string
	Runtime       string
	Metadata      Metadata
	Tests         []TestSource
	RuntimeConfig RawRuntimeConfig
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
	Schema        string         `toml:"schema"`
	Runtime       string         `toml:"runtime"`
	Metadata      *Metadata      `toml:"scenario,omitempty"`
	Tests         []TestSource   `toml:"tests,omitempty"`
	RuntimeConfig map[string]any `toml:"runtime_config,omitempty"`
}

func Parse(data []byte) (Scenario, error) {
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
	s := Scenario{Schema: wire.Schema, Runtime: wire.Runtime, Tests: wire.Tests, RuntimeConfig: config}
	if wire.Metadata != nil {
		s.Metadata = *wire.Metadata
	}
	return Canonicalize(s)
}

var runtimePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*/v[1-9][0-9]*$`)

func RuntimeID(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func Canonicalize(s Scenario) (Scenario, error) {
	out := s
	out.Schema = strings.ToLower(strings.TrimSpace(s.Schema))
	out.Runtime = RuntimeID(s.Runtime)
	if out.Schema != Schema {
		return Scenario{}, errors.New("unsupported scenario schema")
	}
	if !runtimePattern.MatchString(out.Runtime) {
		return Scenario{}, errors.New("invalid or missing runtime identifier")
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
	if err := ValidateGeneric(s); err != nil {
		return err
	}
	for i, source := range s.Tests {
		if source.File != nil {
			if _, err := SourcePath(workspace, *source.File); err != nil {
				return fmt.Errorf("test[%d]: %w", i, err)
			}
		}
	}
	return nil
}

func SerializeCanonical(s Scenario) ([]byte, error) {
	s, err := Canonicalize(s)
	if err != nil {
		return nil, err
	}
	var config map[string]any
	if err := toml.Unmarshal(s.RuntimeConfig.Bytes(), &config); err != nil {
		return nil, errors.New("invalid runtime configuration")
	}
	wire := document{Schema: s.Schema, Runtime: s.Runtime, Tests: s.Tests, RuntimeConfig: config}
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
	if committed && o.Tests != nil {
		return Scenario{}, errors.New("scenario file cannot be combined with author-provided test sources")
	}
	if o.Runtime != nil {
		if committed && RuntimeID(*o.Runtime) != RuntimeID(base.Runtime) {
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
	return Canonicalize(base)
}
