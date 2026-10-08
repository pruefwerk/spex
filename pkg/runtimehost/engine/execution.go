package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pruefwerk/spex/pkg/ownedkind"
	"github.com/pruefwerk/spex/pkg/runtimehost"
	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
	"github.com/pruefwerk/spex/pkg/scenario"
)

type executionPaths struct {
	Scope      string `json:"scope"`
	Suite      string `json:"SPEX_SUITE"`
	Kubeconfig string `json:"KUBECONFIG"`
}

func (h *Host) supportedSuite(suite string) error {
	if filepath.Base(suite) != filepath.Base(h.config.Suite) {
		return errors.New("suite has no qualified execution-owned lifecycle")
	}
	return nil
}

func (h *Host) prepareExecution(root, suite string, env func(string) string) (executionPaths, error) {
	if err := h.supportedSuite(suite); err != nil {
		return executionPaths{}, err
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return executionPaths{}, err
	}
	repo := env("GITHUB_REPOSITORY")
	if repo == "" {
		repo = h.config.LocalRepository
	}
	integer := func(key string) (int64, error) {
		value := env(key)
		if value == "" {
			return 1, nil
		}
		return strconv.ParseInt(value, 10, 64)
	}
	run, err := integer("GITHUB_RUN_ID")
	if err != nil {
		return executionPaths{}, errors.New("invalid run identity")
	}
	attempt, err := integer("GITHUB_RUN_ATTEMPT")
	if err != nil {
		return executionPaths{}, errors.New("invalid attempt identity")
	}
	request := make([]byte, 16)
	if _, err := rand.Read(request); err != nil {
		return executionPaths{}, err
	}
	scope, err := h.resolver.Allocate(repo, run, attempt, hex.EncodeToString(request), "kind")
	if err != nil {
		return executionPaths{}, err
	}
	name, _ := scope.Name()
	dir := filepath.Join(root, ".spex/execution-scopes")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return executionPaths{}, err
	}
	path := filepath.Join(dir, name+".json")
	if err := hostconfig.SaveScope(path, scope); err != nil {
		return executionPaths{}, err
	}
	generated, err := h.configureKind(root, path)
	if err != nil {
		return executionPaths{}, err
	}
	return executionPaths{path, generated, filepath.Join(root, ".spex/isolated", name, "kubeconfig")}, nil
}

// Both admitted receiver runs and split CI steps bind to the same projected
// documents. A scope identity alone does not authorize a different suite.
func (h *Host) checkExecution(root string, paths executionPaths, runner ownedkind.Runner) (*ownedkind.Cluster, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	cluster, err := h.nativeKind(root, paths.Scope, runner)
	if err != nil {
		return nil, err
	}
	scope, err := h.resolver.LoadScope(paths.Scope)
	if err != nil {
		return nil, err
	}
	name, _ := scope.Name()
	if paths.Suite != filepath.Join(root, name+".yaml") || paths.Kubeconfig != filepath.Join(root, ".spex/isolated", name, "kubeconfig") {
		return nil, errors.New("execution paths do not belong to scope")
	}
	var policy struct {
		Identity string `json:"identity"`
	}
	if readJSON(filepath.Join(root, ".spex/isolated", name, "host-policy.json"), &policy) != nil ||
		policy.Identity != hostconfig.Identity(h.config) {
		return nil, errors.New("runtime host policy changed after preparation")
	}
	suite, err := readYAML(paths.Suite)
	if err != nil {
		return nil, err
	}
	bindingRef, err := scalarAt(suite, "spec", "bindingRef")
	if err != nil {
		return nil, err
	}
	profileRef, err := scalarAt(suite, "spec", "integrationProfileRef")
	if err != nil {
		return nil, err
	}
	if bindingRef.Value != "bindings/"+name+".yaml" || profileRef.Value != "integration/"+name+".yaml" {
		return nil, errors.New("execution references unowned configuration")
	}
	binding, err := readYAML(filepath.Join(root, bindingRef.Value))
	if err != nil {
		return nil, err
	}
	profile, err := readYAML(filepath.Join(root, profileRef.Value))
	if err != nil {
		return nil, err
	}
	kubeContext, err := scalarAt(binding, "spec", "kubeContext")
	if err != nil {
		return nil, err
	}
	clusterName, err := scalarAt(profile, "spec", "kind", "clusterName")
	if err != nil {
		return nil, err
	}
	start, err := nodeAt(profile, "spec", "kind", "start")
	if err != nil || start.Tag != "!!bool" || start.Value != "false" || kubeContext.Value != "kind-"+name || clusterName.Value != name {
		return nil, errors.New("configuration escapes execution scope")
	}
	return cluster, nil
}

func (h *Host) capacityIdentity(root, scopePath string, env func(string) string) (capacityContext, string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return capacityContext{}, "", err
	}
	if _, err := h.nativeKind(root, scopePath, nil); err != nil {
		return capacityContext{}, "", err
	}
	scope, err := h.resolver.LoadScope(scopePath)
	if err != nil {
		return capacityContext{}, "", err
	}
	name, _ := scope.Name()
	mode := "disabled"
	_, uri, err := h.resolveScheduling(root, env)
	if err != nil {
		return capacityContext{}, "", err
	}
	if uri != "" {
		mode = "enabled"
	}
	value := capacityContext{
		Schema: h.resolver.Protocol("capacity-context"), Root: root, ScopeName: name, ScopePath: scopePath,
		Worker:  fmt.Sprintf("%s/%d/%d/%s", scope.Repository, scope.RunID, scope.Attempt, name),
		Request: scope.Request, Mode: mode,
	}
	return value, filepath.Join(root, ".spex/capacity", name), nil
}

type capacityHost func(context.Context, string, string) int

func (h *Host) gateCapacity(ctx context.Context, operation, root, scope string, env func(string) string, host capacityHost, cleanup func(context.Context, string, string) error) error {
	value, dir, err := h.capacityIdentity(root, scope, env)
	if err != nil {
		return err
	}
	session := runtimehost.Session{
		Identity: value, Directory: dir, CleanupSchema: h.resolver.Protocol("capacity-cleanup"),
		ScheduledOperation: func(ctx context.Context, operation runtimehost.Operation, path string) error {
			if host == nil || host(ctx, "capacity-"+string(operation), path) != 0 {
				return runtimehost.ErrAdmission
			}
			return nil
		},
	}
	if cleanup != nil {
		session.Cleanup = func(ctx context.Context) error { return cleanup(ctx, value.Root, scope) }
	}
	return session.Gate(ctx, runtimehost.Operation(operation))
}

func (h *Host) executionCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("execution operation required")
	}
	flags := flag.NewFlagSet("execution", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", ".", "runtime checkout")
	suite := flags.String("suite", os.Getenv("SPEX_SUITE"), "suite input")
	scope := flags.String("scope", os.Getenv(h.config.ScopeEnvironment), "execution scope")
	kubeconfig := flags.String("kubeconfig", os.Getenv("KUBECONFIG"), "private kubeconfig")
	kubeContext := flags.String("context", "", "expected Kubernetes context")
	source := flags.String("scenario", "", "committed scenario within runtime checkout")
	group := flags.String("ci-group", "", "one CI subgroup")
	selectedGroups := flags.String("groups", "", "comma-separated groups for one-shot execution")
	artifactDirectory := flags.String("artifact-directory", ".spex/ci-runs", "private group evidence directory within checkout")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return errors.New("invalid execution flags")
	}
	if *selectedGroups != "" && args[0] != "run" && args[0] != "explain" {
		return errors.New("group selection requires one-shot run")
	}
	absoluteRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	current, err := os.Getwd()
	if err != nil || current != absoluteRoot {
		return errors.New("run execution commands from the runtime checkout")
	}
	*root = absoluteRoot
	if *suite == "" {
		*suite = h.config.Suite
	}
	if args[0] == "supported" {
		return h.supportedSuite(*suite)
	}
	if args[0] == "prepare" {
		paths, err := h.prepareExecution(*root, *suite, os.Getenv)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(map[string]string{h.config.ScopeEnvironment: paths.Scope, "SPEX_SUITE": paths.Suite, "KUBECONFIG": paths.Kubeconfig})
	}
	if args[0] == "prepare-start" {
		paths, err := h.prepareExecution(*root, *suite, os.Getenv)
		if err != nil {
			return err
		}
		// Persist recovery identity before creating any external resources. CI's
		// following steps read these values directly, without shell eval or Python.
		if err := h.writeExecutionEnvironment(os.Getenv("GITHUB_ENV"), paths); err != nil {
			return err
		}
		if err := os.Setenv(h.config.ScopeEnvironment, paths.Scope); err != nil {
			return err
		}
		if err := os.Setenv("SPEX_SUITE", paths.Suite); err != nil {
			return err
		}
		if err := os.Setenv("KUBECONFIG", paths.Kubeconfig); err != nil {
			return err
		}
		return h.startExecution(ctx, *root, paths)
	}
	if args[0] == "explain" {
		paths, err := h.prepareExecution(*root, *suite, os.Getenv)
		if err != nil {
			return err
		}
		prepared, err := h.prepareSelectedHostInput(ctx, *root, paths, *source, *selectedGroups)
		if err != nil {
			return err
		}
		id, err := scenario.Identity(prepared.Scenario)
		if err != nil {
			return err
		}
		canonical, err := scenario.SerializeCanonical(prepared.Scenario)
		if err != nil {
			return err
		}
		path := strings.TrimSuffix(paths.Scope, ".json") + ".toml"
		if err := writePrivate(path, canonical); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(struct {
			ScenarioID   string `json:"scenario_id"`
			ScenarioPath string `json:"scenario_path"`
			Plan         any    `json:"plan"`
		}{id, path, prepared.Plan.Summary()})
	}
	if args[0] == "run" {
		if *group != "" {
			return errors.New("CI group requires group-run")
		}
		if *scope != "" {
			return errors.New("one-shot execution requires a fresh scope")
		}
		paths, err := h.prepareExecution(*root, *suite, os.Getenv)
		if err != nil {
			return err
		}
		result, err := h.runSelectedHostInput(ctx, *root, paths, *source, *selectedGroups)
		if result.Schema != "" {
			if encodeErr := json.NewEncoder(stdout).Encode(result); encodeErr != nil && err == nil {
				err = encodeErr
			}
		}
		return err
	}
	if args[0] == "group-run" {
		names, err := hostconfig.Names(*group)
		if err != nil || (*group != "" && len(names) != 1) {
			return errors.New("group-run accepts one CI group")
		}
		result, artifacts, err := h.runHostGroupAt(ctx, *root, executionPaths{*scope, *suite, *kubeconfig}, *source, *group, *artifactDirectory)
		if artifacts != "" {
			fmt.Fprintln(os.Stderr, "Group artifacts:", artifacts)
		}
		if result.Schema != "" {
			if encodeErr := json.NewEncoder(stdout).Encode(result); encodeErr != nil && err == nil {
				err = encodeErr
			}
		}
		return err
	}
	if args[0] == "cleanup" {
		if *scope == "" {
			return nil
		}
		cleanup, cancel := context.WithTimeout(context.Background(), hostconfig.Deadline(h.config.CleanupTimeout))
		defer cancel()
		return h.gateCapacity(cleanup, "finish", *root, *scope, os.Getenv, h.capacityCommand, func(ctx context.Context, root, scope string) error {
			return h.kindOperation(ctx, root, scope, "cleanup")
		})
	}
	if args[0] != "start" && args[0] != "check" && args[0] != "require" && args[0] != "profile" {
		return errors.New("unknown execution operation")
	}
	cluster, err := h.checkExecution(*root, executionPaths{*scope, *suite, *kubeconfig}, nil)
	if err != nil {
		return err
	}
	if args[0] == "start" {
		return h.startExecution(ctx, *root, executionPaths{*scope, *suite, *kubeconfig})
	}
	if args[0] == "check" {
		if err := h.gateCapacity(ctx, "check", *root, *scope, os.Getenv, h.capacityCommand, nil); err != nil {
			return err
		}
	}
	nodes, err := cluster.OwnedNodes(ctx)
	if err != nil || len(nodes) == 0 {
		return errors.New("execution has no live owned nodes")
	}
	if args[0] == "profile" {
		value, _, err := h.capacityIdentity(*root, *scope, os.Getenv)
		if err != nil || *kubeContext != "kind-"+value.ScopeName {
			return errors.New("profile targets another execution")
		}
	}
	return nil
}

func (h *Host) startExecution(ctx context.Context, root string, paths executionPaths) error {
	cluster, err := h.checkExecution(root, paths, nil)
	if err != nil {
		return err
	}
	if _, err := h.prepareHostInput(ctx, root, paths, ""); err != nil {
		return err
	}
	if err := h.gateCapacity(ctx, "acquire", root, paths.Scope, os.Getenv, h.capacityCommand, nil); err != nil {
		return err
	}
	return cluster.Create(ctx)
}

func (h *Host) writeExecutionEnvironment(path string, paths executionPaths) error {
	if path == "" {
		return errors.New("CI environment output required")
	}
	for _, value := range []string{paths.Scope, paths.Suite, paths.Kubeconfig} {
		if strings.ContainsAny(value, "\r\n") {
			return errors.New("invalid CI environment value")
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("CI environment output unavailable")
	}
	defer file.Close()
	if _, err := fmt.Fprintf(file, "%s=%s\nSPEX_SUITE=%s\nKUBECONFIG=%s\n", h.config.ScopeEnvironment, paths.Scope, paths.Suite, paths.Kubeconfig); err != nil {
		return err
	}
	return file.Sync()
}
