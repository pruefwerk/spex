package spex

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

func TestMigrationSelectionArraysReplaceAndClear(t *testing.T) {
	base := overlayTOML(t, "[selection]\ninclude_tags = ['old']\ninclude_any_tags = ['first','second']\nexclude_tags = ['excluded']")
	override := overlayTOML(t, "[selection]\ninclude_tags = []\ninclude_any_tags = ['replacement']")
	merged, err := (migrationRuntime{}).MergeConfig(base, override)
	if err != nil {
		t.Fatal(err)
	}
	o, err := decodeMigrationOverlay(merged)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := o.ResolveControls(suiteFlags{includeTags: stringListFlag{"inherited"}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if o.Selection.IncludeTags == nil || len(flags.includeTags) != 0 || !reflect.DeepEqual(flags.includeAnyTags, stringListFlag{"replacement"}) || !reflect.DeepEqual(flags.excludeTags, stringListFlag{"excluded"}) {
		t.Fatalf("incorrect replacement: %+v", flags)
	}
	flags.includeAnyTags[0] = "mutated"
	if (*o.Selection.IncludeAnyTags)[0] != "replacement" {
		t.Fatal("flags alias overlay")
	}
}

func TestMigrationSelectionUsesExistingFilters(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	flags, err := parseSuiteFlags("run", []string{"--suite", filepath.Join(root, "examples/suites/mqtt-local.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	r := scenarioruntime.NewRegistry()
	_ = r.Register(migrationRuntime{flags: flags})
	document := scenario.Scenario{Schema: scenario.Schema, Runtime: "migration-testbench/v1", RuntimeConfig: overlayTOML(t, "[selection]\ninclude_any_tags = ['regression']")}
	p, err := r.Prepare(context.Background(), scenarioruntime.ResolveRequest{Scenario: document, Workspace: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Plan.Summary().Tests) != 2 {
		t.Fatal("selection no longer matches legacy regression filter")
	}
}

func TestMigrationHookRenewsEachTestWithoutPersistingCredentials(t *testing.T) {
	root := t.TempDir()
	const secret = "SENTINEL_RENEWED_CREDENTIAL"
	counter := filepath.Join(root, "calls")
	hook := "#!/bin/sh\nprintf 'call\\n' >> " + shellQuote(counter) + "\nprintf '%s' '{\"SPEX_TEST_CREDENTIAL\":\"" + secret + "\"}'\n"
	if err := os.WriteFile(filepath.Join(root, "renew"), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(root, "kubectl")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\n[ \"$SPEX_TEST_CREDENTIAL\" = "+shellQuote(secret)+" ] || exit 9\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	flags, err := parseSuiteFlags("run", []string{"--suite", filepath.Join(repoRoot(t), "examples/suites/mqtt-local.yaml"), "--command", runner})
	if err != nil {
		t.Fatal(err)
	}
	r := scenarioruntime.NewRegistry()
	_ = r.Register(migrationRuntime{flags: flags})
	inline := scenarioSmokeSource
	// This checks renewal and secret handling, not timeout enforcement. Allow
	// process startup under release-build load; dedicated hook tests cover expiry.
	document := scenario.Scenario{Schema: scenario.Schema, Runtime: "migration-testbench/v1", Tests: []scenario.TestSource{{Inline: &inline}}, RuntimeConfig: overlayTOML(t, "[execution]\nrepetitions = 2\nbefore_scenario_hook = 'renew'\nbefore_scenario_hook_timeout = '10s'")}
	p, err := r.Prepare(context.Background(), scenarioruntime.ResolveRequest{Scenario: document, Workspace: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(counter); !os.IsNotExist(err) {
		t.Fatal("planning executed hook")
	}
	id, _ := scenario.Identity(document)
	sink, err := scenarioruntime.NewFileArtifacts(root, "artifacts", id)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	result, err := scenarioruntime.Run(context.Background(), p, sink)
	if err != nil || result.Outcome != scenarioruntime.Passed {
		t.Fatalf("hook execution failed: %+v %v", result, err)
	}
	calls, err := os.ReadFile(counter)
	if err != nil || strings.Count(string(calls), "call") != 2 {
		t.Fatal("hook did not run once per test")
	}
	if os.Getenv("SPEX_TEST_CREDENTIAL") != "" {
		t.Fatal("hook mutated process environment")
	}
	err = filepath.WalkDir(sink.Directory(), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), secret) {
			t.Fatal("renewed credential persisted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigrationHookValidationAndClearing(t *testing.T) {
	for _, source := range []string{"[execution]\nbefore_scenario_hook='../escape'", "[execution]\nbefore_scenario_hook_timeout='11m'", "[execution]\nbefore_scenario_hook_timeout='0s'"} {
		if _, err := decodeMigrationOverlay(overlayTOML(t, source)); err == nil {
			t.Fatal("accepted invalid hook")
		}
	}
	o, err := decodeMigrationOverlay(overlayTOML(t, "[execution]\nbefore_scenario_hook=''"))
	if err != nil {
		t.Fatal(err)
	}
	flags, err := o.ResolveControls(suiteFlags{beforeScenarioHook: "inherited"}, t.TempDir())
	if err != nil || flags.beforeScenarioHook != "" {
		t.Fatal("explicit clearing failed")
	}
}
