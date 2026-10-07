package scenario

import (
	"bytes"
	"strings"
	"testing"
)

func TestReceiverRuntimeSelection(t *testing.T) {
	merge := func(base, overlay RawRuntimeConfig) (RawRuntimeConfig, error) {
		if !overlay.Empty() {
			return overlay, nil
		}
		return base, nil
	}
	catalog := []RuntimeRelease{{"migration-testbench/v1", "v0.4.0", true, merge}, {"migration-testbench/v1", "v0.3.0", false, merge}, {"other/v1", "v2.0.0", true, merge}}
	for _, tc := range []struct{ selector, contract, release string }{
		{"", "migration-testbench/v1", "v0.4.0"}, {"latest", "migration-testbench/v1", "v0.4.0"},
		{"migration-testbench/v1", "migration-testbench/v1", "v0.4.0"},
		{"migration-testbench/v1@latest", "migration-testbench/v1", "v0.4.0"},
		{"migration-testbench/v1@v0.3.0", "migration-testbench/v1", "v0.3.0"},
		{"other/v1", "other/v1", "v2.0.0"},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			request, err := AuthorRequest(Scenario{}, AuthoringOverrides{Runtime: &tc.selector}, false)
			if err != nil {
				t.Fatal(err)
			}
			data, err := SerializeRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Parse(data); err == nil {
				t.Fatal("unresolved request accepted as executable scenario")
			}
			request, err = ParseRequest(data)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := ResolveRequest(request, catalog, "migration-testbench/v1")
			if err != nil || resolved.Runtime != tc.contract || resolved.RuntimeRelease != tc.release {
				t.Fatalf("resolution=%+v err=%v", resolved, err)
			}
			canonical, err := SerializeCanonical(resolved)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(data, canonical) {
				t.Fatal("request and resolved identities must differ")
			}
			if _, err := Parse(canonical); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, selector := range []string{"unknown/v1", "migration-testbench/v1@v9.0.0"} {
		if _, err := ResolveRequest(Scenario{Schema: RequestSchema, Runtime: selector}, catalog, "migration-testbench/v1"); err == nil {
			t.Fatal("unsupported selection fell back")
		}
	}
	if _, err := ResolveRequest(Scenario{Schema: RequestSchema}, append(catalog, catalog[0]), "migration-testbench/v1"); err == nil {
		t.Fatal("ambiguous default accepted")
	}
}

func TestRequestDefersTypedConfigurationMerge(t *testing.T) {
	base, _ := ParseRuntimeConfig([]byte("enabled=true"))
	overlay, _ := ParseRuntimeConfig([]byte("enabled=false"))
	request, err := AuthorRequest(Scenario{RuntimeConfig: base}, AuthoringOverrides{RuntimeConfig: &overlay}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(request.RuntimeConfig.Bytes(), base.Bytes()) || !bytes.Equal(request.RuntimeOverlay.Bytes(), overlay.Bytes()) {
		t.Fatal("caller merged receiver configuration")
	}
	called := false
	resolved, err := ResolveRequest(request, []RuntimeRelease{{"test/v1", "v1.2.0", true, func(b, o RawRuntimeConfig) (RawRuntimeConfig, error) { called = true; return o, nil }}}, "test/v1")
	if err != nil || !called || !resolved.RuntimeOverlay.Empty() || !strings.Contains(string(resolved.RuntimeConfig.Bytes()), "false") {
		t.Fatalf("typed merge failed: %v", err)
	}
}

func TestCommittedRequestCannotChangePinnedRuntime(t *testing.T) {
	for _, selector := range []string{"other/v1", "test/v1@v9"} {
		if _, err := AuthorRequest(Scenario{Runtime: "test/v1", RuntimeRelease: "v1"}, AuthoringOverrides{Runtime: &selector}, true); err == nil {
			t.Fatal("changed committed runtime pin")
		}
	}
}
