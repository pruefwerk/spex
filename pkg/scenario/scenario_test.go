package scenario

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ptr(value string) *string { return &value }

const minimal = "schema = 'spex.scenario/v1'\nruntime = 'migration-testbench/v1'\n"

func TestParseAndCanonicalRoundTrip(t *testing.T) {
	for _, extra := range []string{"", "[scenario]\nname='example'\ndescription='test'\ntimeout='15m'\n", "[[tests]]\nfile='acceptance/test.yaml'\n", "[[tests]]\ninline='Feature: smoke'\n", "[[tests]]\nfile='a.feature'\n[[tests]]\ninline='Feature: b'\n[runtime_config]\nprofile='existing'\n[runtime_config.environment]\nreuse=false\n"} {
		t.Run(extra, func(t *testing.T) {
			original, err := Parse([]byte(minimal + extra))
			if err != nil {
				t.Fatal(err)
			}
			first, err := SerializeCanonical(original)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := Parse(first)
			if err != nil {
				t.Fatal(err)
			}
			second, err := SerializeCanonical(parsed)
			if err != nil || !bytes.Equal(first, second) {
				t.Fatalf("round trip changed: %s / %s (%v)", first, second, err)
			}
			a, _ := Identity(original)
			b, _ := Identity(parsed)
			if a != b || len(a) != 64 {
				t.Fatal("unstable identity")
			}
		})
	}
}

func TestParseRejectsInvalidDocumentsWithoutExposingInput(t *testing.T) {
	for _, input := range []string{
		"schema='wrong'\nruntime='migration-testbench/v1'", "schema='spex.scenario/v1'", "schema=",
		minimal + "surprise='sentinel-private-value'", minimal + "[scenario]\ntimeout='sentinel-private-value'",
		minimal + "[scenario]\ntimeout='0s'", minimal + "[scenario]\ntimeout='-1s'",
		minimal + "[[tests]]\nfile='x'\ninline='y'", minimal + "[[tests]]\nname='empty'",
		minimal + "[[tests]]\ninline=''", minimal + "[[tests]]\nfile='../outside'",
		minimal + "[[tests]]\nfile='/tmp/private'", minimal + "[[tests]]\nfile='C:\\private'",
		minimal + "[[tests]]\nfile='x'\ntypo='sentinel-private-value'",
	} {
		_, err := Parse([]byte(input))
		if err == nil {
			t.Errorf("accepted invalid input: %s", input)
		} else if strings.Contains(err.Error(), "sentinel-private-value") {
			t.Fatal("input leaked into error")
		}
	}
}

func TestCanonicalizationNormalizesWithoutMutatingSource(t *testing.T) {
	raw, _ := ParseRuntimeConfig([]byte("z=1\na=false"))
	original := Scenario{Schema: " SPEX.SCENARIO/V1 ", Runtime: " Migration-Testbench/V1 ", Metadata: Metadata{Timeout: ptr("60s")}, Tests: []TestSource{{File: ptr("acceptance/./a/../b.feature")}, {Inline: ptr("Feature: x\r\nScenario: y\r")}}, RuntimeConfig: raw}
	normalized, err := Canonicalize(original)
	if err != nil {
		t.Fatal(err)
	}
	if *normalized.Tests[0].File != "acceptance/b.feature" || *normalized.Tests[1].Inline != "Feature: x\nScenario: y\n" || *normalized.Metadata.Timeout != "1m0s" {
		t.Fatal("normalization failed")
	}
	*normalized.Tests[0].File = "changed"
	if *original.Tests[0].File == "changed" {
		t.Fatal("aliased file pointer")
	}
	data := raw.Bytes()
	data[0] = 'x'
	if bytes.Equal(data, raw.Bytes()) {
		t.Fatal("mutable config storage")
	}
	a, _ := ParseRuntimeConfig([]byte("a=false\nz=1"))
	if !bytes.Equal(a.Bytes(), raw.Bytes()) {
		t.Fatal("map order affected canonical TOML")
	}
}

func TestWorkspaceConfinement(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "good.feature"), []byte("Feature: safe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.feature"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"missing.feature", "../secret.feature", "escape/secret.feature", "."} {
		if _, err := SourcePath(root, file); err == nil {
			t.Errorf("accepted %s", file)
		}
	}
	s, _ := Parse([]byte(minimal + "[[tests]]\nfile='good.feature'"))
	if err := ValidateSources(s, root); err != nil {
		t.Fatal(err)
	}
}

func TestBuilderUsesRuntimeMergerAndRejectsAmbiguousSources(t *testing.T) {
	base, _ := Parse([]byte(minimal + "[scenario]\nname='before'\n[[tests]]\nfile='a.feature'\n[runtime_config]\nprofile='base'"))
	overlay, _ := ParseRuntimeConfig([]byte("profile='selected'"))
	called := false
	merged, err := MergeAuthoringOverrides(base, AuthoringOverrides{Name: ptr("$(printf never)"), Description: ptr(""), Runtime: ptr("MIGRATION-TESTBENCH/V1"), RuntimeConfig: &overlay}, true, func(a, b RawRuntimeConfig) (RawRuntimeConfig, error) {
		called = true
		if a.Empty() {
			t.Fatal("lost committed config")
		}
		return b, nil
	})
	if err != nil || !called || merged.Metadata.Name != "$(printf never)" || base.Metadata.Name != "before" {
		t.Fatalf("override failed: %v", err)
	}
	if _, err := MergeAuthoringOverrides(base, AuthoringOverrides{Runtime: ptr("other/v1")}, true, nil); err == nil {
		t.Fatal("accepted runtime change")
	}
	if _, err := MergeAuthoringOverrides(base, AuthoringOverrides{Tests: []TestSource{}}, true, nil); err == nil {
		t.Fatal("accepted external test sources")
	}
	built, err := Build("migration-testbench/v1", AuthoringOverrides{Tests: []TestSource{{File: ptr("a.feature")}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	manual, _ := Parse([]byte(minimal + "[[tests]]\nfile='a.feature'"))
	a, _ := Identity(built)
	b, _ := Identity(manual)
	if a != b {
		t.Fatal("builder identity differs")
	}
}

func TestStrictRuntimeDecodeDoesNotInterpretOrLeakFields(t *testing.T) {
	raw, err := ParseRuntimeConfig([]byte("profiel='sentinel-private-value'"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Profile string `toml:"profile"`
	}
	err = raw.DecodeStrict(&config)
	if err == nil || strings.Contains(err.Error(), "sentinel-private-value") {
		t.Fatal("unsafe or permissive runtime decoding")
	}
}

func TestCanonicalIdentityIndependentOfWorkspace(t *testing.T) {
	source := minimal + "[[tests]]\nfile='acceptance/test.feature'\n"
	a, _ := Parse([]byte(source))
	b, _ := Parse([]byte(strings.ReplaceAll(source, "\n", "\r\n")))
	x, _ := Identity(a)
	y, _ := Identity(b)
	if x != y {
		t.Fatal("line endings changed identity")
	}
	data, _ := SerializeCanonical(a)
	for _, forbidden := range []string{"/Users/", "/home/runner/", "runId", "timestamp"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatal("execution context in identity")
		}
	}
}
