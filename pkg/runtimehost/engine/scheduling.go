package engine

import (
	"context"
	"errors"
	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pruefwerk/spex/pkg/receiver"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
	"github.com/pruefwerk/spex/pkg/scheduling"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type schedulingPolicy = hostconfig.SchedulingPolicy

// Resolve an exact environment reference, not a general template. Older host
// policies retain their existing environment-variable convention.
func (h *Host) resolveScheduling(root string, env func(string) string) (schedulingPolicy, string, error) {
	var policy schedulingPolicy
	legacy := env("SPEX_SCHEDULING_MONGODB_URI")
	path := filepath.Join(root, h.config.SchedulingPolicy)
	if readJSON(path, &policy) != nil {
		if _, err := os.Stat(path); os.IsNotExist(err) && legacy == "" {
			return policy, "", nil
		}
		return policy, "", errors.New("runtime scheduling policy invalid")
	}
	name := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	if !h.resolver.IsSchedulingPolicySchema(policy.Schema) || !name.MatchString(policy.Database) || !name.MatchString(policy.Collection) || policy.Pool != h.config.SchedulingPool || policy.Capacity < 1 || policy.Capacity > 64 {
		return policy, "", errors.New("runtime scheduling policy invalid")
	}
	uri := legacy
	if policy.ConnectionString != nil {
		uri = *policy.ConnectionString
		if strings.HasPrefix(uri, "${") {
			ref := regexp.MustCompile(`^\$\{([A-Z][A-Z0-9_]*)\}$`).FindStringSubmatch(uri)
			if ref == nil {
				return policy, "", errors.New("scheduling connection reference invalid")
			}
			uri = env(ref[1])
		}
	}
	if uri != "" && !strings.HasPrefix(uri, "mongodb://") && !strings.HasPrefix(uri, "mongodb+srv://") {
		return policy, "", errors.New("scheduling connection string invalid")
	}
	return policy, uri, nil
}

// The runtime owns pool identity and capacity; caller definitions cannot select
// either. MongoDB must outlive the test clusters and be shared by all workers.
// An absent URI disables capacity scheduling. A configured but invalid or
// unavailable store must never silently fall back to unscheduled execution.
func (h *Host) openScheduling(ctx context.Context, root, worker string) (*receiver.Scheduling, func(), error) {
	policy, uri, err := h.resolveScheduling(root, os.Getenv)
	if err != nil {
		return nil, nil, err
	}
	if uri == "" {
		return nil, func() {}, nil
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetServerSelectionTimeout(10 * time.Second).SetConnectTimeout(10 * time.Second))
	if err != nil {
		return nil, nil, errors.New("shared scheduling store unavailable")
	}
	close := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Disconnect(cleanup)
	}
	check, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if client.Ping(check, nil) != nil {
		close()
		return nil, nil, errors.New("shared scheduling store unavailable")
	}
	store, err := scheduling.NewMongoStore(client.Database(policy.Database).Collection(policy.Collection), policy.Pool)
	if err != nil {
		close()
		return nil, nil, errors.New("shared scheduling store invalid")
	}
	return &receiver.Scheduling{Scheduler: &scheduling.Scheduler{Store: store, Pool: policy.Pool, Capacity: policy.Capacity, PollInterval: 2 * time.Second}, Worker: worker, Verify: verifyKindCapacity}, close, nil
}

func verifyKindCapacity(_ context.Context, result scenarioruntime.ExecutionResult) error {
	if result.Cleanup != "succeeded" {
		return errors.New("Kind capacity cleanup is not confirmed")
	}
	return nil
}
