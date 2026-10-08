// Package definition decodes trusted runtime-host configuration. It supplies
// typed policy to the host, never replaces inherited suite configuration.
package definition

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const Schema = "spex.runtime-host/v1"
const ConfigEnvironment = "SPEX_RUNTIME_CONFIG"

// Defaults cover host mechanisms only. Projects supply environment references,
// service inventories and application setup values.
func Defaults() Definition {
	return Definition{
		Schema: Schema, Runtime: "migration-testbench/v1",
		ScopePrefix: "spex", SchemaPrefix: "spex", ScopeEnvironment: "SPEX_EXECUTION_SCOPE",
		LocalRepository: "local/runtime", ArtifactManifest: "artifacts.json",
		SourcePolicy: "source-policy.json", SchedulingPolicy: "scheduling-policy.json",
		SchedulingPool: "runtime/kind", CleanupTimeout: "2m", MaximumTimeout: "6h",
		BeforeScenarioHookTimeout: "2m", Groups: []string{"baseline"}, BaselineTag: "ci-baseline",
		SummaryTitle: "Runtime groups", ImageRepository: "spex-fixture",
	}
}

type Build struct {
	Context         string            `toml:"context"`
	Dockerfile      string            `toml:"dockerfile"`
	Arguments       map[string]string `toml:"arguments"`
	MirrorArguments map[string]string `toml:"mirror_arguments"`
	Contexts        map[string]string `toml:"contexts"`
}
type Image struct {
	Alias      string `toml:"alias"`
	ProbeAlias bool   `toml:"probe_alias"`
	Build      Build  `toml:"build"`
}
type TagOverride struct {
	Tag       string            `toml:"tag"`
	Profiles  []string          `toml:"profiles"`
	Exclusive string            `toml:"exclusive"`
	Values    map[string]string `toml:"values"`
}
type Tags struct {
	Prefix    string                       `toml:"prefix"`
	Reserved  []string                     `toml:"reserved"`
	Defaults  map[string]string            `toml:"defaults"`
	Profiles  map[string]map[string]string `toml:"profiles"`
	Overrides []TagOverride                `toml:"overrides"`
}
type Readiness struct {
	Namespace      string   `toml:"namespace"`
	Selector       string   `toml:"selector"`
	Container      string   `toml:"container"`
	Message        string   `toml:"message"`
	Sources        []string `toml:"sources"`
	Replicas       int      `toml:"replicas"`
	TimeoutSeconds int      `toml:"timeout_seconds"`
}
type Audit struct {
	ID                []string `toml:"id"`
	Parent            []string `toml:"parent"`
	Value             []string `toml:"value"`
	OutputID          string   `toml:"output_id"`
	OutputParent      string   `toml:"output_parent"`
	OutputValue       string   `toml:"output_value"`
	OutputParentValue string   `toml:"output_parent_value"`
	MissingCondition  string   `toml:"missing_condition"`
	ConflictCondition string   `toml:"conflict_condition"`
}
type Credentials struct {
	AccountEnvironment string `toml:"account_environment"`
	RegionEnvironment  string `toml:"region_environment"`
	Role               string `toml:"role"`
	Session            string `toml:"session"`
	DurationSeconds    int    `toml:"duration_seconds"`
}
type Definition struct {
	Schema                    string            `toml:"schema"`
	Runtime                   string            `toml:"runtime"`
	Suite                     string            `toml:"suite"`
	Binding                   string            `toml:"binding"`
	Profile                   string            `toml:"profile"`
	Topology                  string            `toml:"topology"`
	Namespace                 string            `toml:"namespace"`
	ScopePrefix               string            `toml:"scope_prefix"`
	SchemaPrefix              string            `toml:"schema_prefix"`
	ScopeEnvironment          string            `toml:"scope_environment"`
	LocalRepository           string            `toml:"local_repository"`
	ArtifactManifest          string            `toml:"artifact_manifest"`
	SourcePolicy              string            `toml:"source_policy"`
	SchedulingPolicy          string            `toml:"scheduling_policy"`
	SchedulingPool            string            `toml:"scheduling_pool"`
	CleanupTimeout            string            `toml:"cleanup_timeout"`
	MaximumTimeout            string            `toml:"maximum_timeout"`
	BeforeScenarioHook        string            `toml:"before_scenario_hook"`
	BeforeScenarioHookTimeout string            `toml:"before_scenario_hook_timeout"`
	Groups                    []string          `toml:"groups"`
	BaselineTag               string            `toml:"baseline_tag"`
	SummaryTitle              string            `toml:"summary_title"`
	HealthResources           []string          `toml:"health_resources"`
	MirrorInventory           string            `toml:"mirror_inventory"`
	MirrorPrefix              string            `toml:"mirror_prefix"`
	ImageRepository           string            `toml:"image_repository"`
	ImageServices             []string          `toml:"image_services"`
	ImageInputs               []string          `toml:"image_inputs"`
	Images                    []Image           `toml:"images"`
	ResourceTypes             map[string]string `toml:"resource_types"`
	Tags                      Tags              `toml:"tags"`
	Readiness                 Readiness         `toml:"readiness"`
	Audit                     Audit             `toml:"audit"`
	Credentials               Credentials       `toml:"credentials"`
}

var active atomic.Pointer[Definition]

// Bind installs one validated definition for a CLI host process. The executable
// starts one host per process; this is not an in-process multi-host registry.
func Bind(d Definition) {
	// Copy nested policy maps and slices too; caller mutations must not change
	// the definition used by a running host.
	copy := Clone(d)
	active.Store(&copy)
}

// Clone protects nested maps and slices at public configuration boundaries.
func Clone(d Definition) Definition {
	data, _ := json.Marshal(d)
	var copy Definition
	_ = json.Unmarshal(data, &copy)
	return copy
}

func Identity(d Definition) string {
	// JSON orders map keys deterministically. Only typed policy data contributes
	// to this identity, never the checkout path or expanded credentials.
	data, _ := json.Marshal(d)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func Current() Definition {
	if d := active.Load(); d != nil {
		return Clone(*d)
	}
	return Definition{}
}
func Protocol(kind string) string         { return Current().SchemaPrefix + "." + kind + "/v1" }
func ScopePattern() string                { return "^" + regexp.QuoteMeta(Current().ScopePrefix) + "-[a-f0-9]{32}$" }
func Deadline(value string) time.Duration { duration, _ := time.ParseDuration(value); return duration }

func relative(value string) bool {
	clean := filepath.Clean(value)
	return value != "" && !filepath.IsAbs(value) && clean != ".." &&
		!strings.HasPrefix(clean, ".."+string(filepath.Separator)) && !strings.ContainsAny(value, "\x00\r\n")
}
func Load(path string) (Definition, error) {
	d := Defaults()
	file, err := os.Open(path)
	if err != nil {
		return d, errors.New("runtime host definition unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return d, errors.New("runtime host definition invalid")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 1<<20 {
		return d, errors.New("runtime host definition invalid")
	}
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&d) != nil {
		return d, errors.New("runtime host definition invalid or contains unknown fields")
	}
	return d, d.Validate()
}
func (d Definition) Validate() error {
	invalid := errors.New("runtime host policy incomplete or invalid")
	name := regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	env := regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	if d.Schema != Schema || d.Runtime != "migration-testbench/v1" || !name.MatchString(d.ScopePrefix) ||
		!name.MatchString(d.SchemaPrefix) || !env.MatchString(d.ScopeEnvironment) || d.Namespace == "" ||
		d.LocalRepository == "" || len(d.Groups) == 0 || d.BaselineTag == "" || d.SummaryTitle == "" ||
		d.ImageRepository == "" || d.SchedulingPool == "" {
		return invalid
	}
	for _, path := range []string{d.Suite, d.Binding, d.Profile, d.Topology, d.ArtifactManifest, d.SourcePolicy, d.SchedulingPolicy} {
		if !relative(path) {
			return invalid
		}
	}
	for _, path := range []string{d.MirrorInventory, d.BeforeScenarioHook} {
		if path != "" && !relative(path) {
			return invalid
		}
	}
	for _, duration := range []string{d.CleanupTimeout, d.MaximumTimeout, d.BeforeScenarioHookTimeout} {
		value, err := time.ParseDuration(duration)
		if err != nil || value <= 0 || value > 24*time.Hour {
			return invalid
		}
	}
	seen := map[string]bool{}
	for _, group := range d.Groups {
		if !name.MatchString(group) || seen[group] {
			return invalid
		}
		seen[group] = true
	}
	if !seen["baseline"] {
		return invalid
	}
	if (d.Tags.Prefix != "" || len(d.Tags.Profiles) != 0 || len(d.Tags.Defaults) != 0) &&
		(d.Tags.Prefix == "" || len(d.Tags.Profiles) == 0 || len(d.Tags.Defaults) == 0) {
		return invalid
	}
	checkValues := func(values map[string]string) bool {
		for key, value := range values {
			if !env.MatchString(key) || strings.ContainsAny(value, "\x00\r\n") {
				return false
			}
		}
		return true
	}
	if !checkValues(d.Tags.Defaults) {
		return invalid
	}
	for key, values := range d.Tags.Profiles {
		if !name.MatchString(key) || !checkValues(values) {
			return invalid
		}
		for field := range values {
			if _, ok := d.Tags.Defaults[field]; !ok {
				return invalid
			}
		}
	}
	tags := map[string]bool{}
	for _, override := range d.Tags.Overrides {
		if override.Tag == "" || tags[override.Tag] || !checkValues(override.Values) {
			return invalid
		}
		tags[override.Tag] = true
		for field := range override.Values {
			if _, ok := d.Tags.Defaults[field]; !ok {
				return invalid
			}
		}
		for _, profile := range override.Profiles {
			if _, ok := d.Tags.Profiles[profile]; !ok {
				return invalid
			}
		}
	}
	if (len(d.Images) != 0 || len(d.ImageServices) != 0 || d.MirrorInventory != "") && len(d.ImageInputs) == 0 {
		return invalid
	}
	for _, input := range d.ImageInputs {
		if !relative(input) || filepath.Clean(input) == "." {
			return invalid
		}
	}
	for _, image := range d.Images {
		if (image.Alias == "") == !image.ProbeAlias || !relative(image.Build.Context) || !relative(image.Build.Dockerfile) {
			return invalid
		}
		for _, ref := range image.Build.Contexts {
			if !relative(ref) {
				return invalid
			}
		}
	}
	for _, format := range d.ResourceTypes {
		if format != "literal" && format != "hex16_upper" {
			return invalid
		}
	}
	if (d.Readiness.Namespace != "" || d.Readiness.Selector != "" || len(d.Readiness.Sources) != 0) &&
		(d.Readiness.Namespace == "" || d.Readiness.Selector == "" || d.Readiness.Container == "" || d.Readiness.Message == "" ||
			len(d.Readiness.Sources) == 0 || d.Readiness.Replicas < 1 || d.Readiness.TimeoutSeconds < 1) {
		return invalid
	}
	if (len(d.Audit.ID) != 0 || len(d.Audit.Parent) != 0 || len(d.Audit.Value) != 0) &&
		(len(d.Audit.ID) == 0 || len(d.Audit.Parent) == 0 || len(d.Audit.Value) == 0 ||
			d.Audit.OutputID == "" || d.Audit.OutputParent == "" || d.Audit.OutputValue == "" || d.Audit.OutputParentValue == "" ||
			d.Audit.MissingCondition == "" || d.Audit.ConflictCondition == "") {
		return invalid
	}
	if d.Credentials != (Credentials{}) && (!env.MatchString(d.Credentials.AccountEnvironment) || !env.MatchString(d.Credentials.RegionEnvironment) ||
		d.Credentials.Role == "" || d.Credentials.Session == "" || d.Credentials.DurationSeconds < 900 ||
		d.Credentials.DurationSeconds > 43200) {
		return invalid
	}
	return nil
}
