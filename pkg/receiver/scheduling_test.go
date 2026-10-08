package receiver

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pruefwerk/spex/pkg/scenarioruntime"
	"github.com/pruefwerk/spex/pkg/scheduling"
)

func TestReceiverSchedulesAfterPlanningAndReleasesAfterVerification(t *testing.T) {
	for _, safe := range []bool{true, false} {
		for _, outcome := range []scenarioruntime.Outcome{scenarioruntime.Passed, scenarioruntime.Failed, scenarioruntime.Cancelled} {
			request, _, _ := requestFixture(t, Authoring{Definition: "Feature: One"})
			host, runtime, _, sink := hostFixture()
			runtime.outcome = outcome
			scheduler := &scheduling.Scheduler{Store: &scheduling.MemoryStore{}, Pool: "kind/test", Capacity: 1}
			host.Scheduling = &Scheduling{Scheduler: scheduler, Worker: "worker", Verify: func(ctx context.Context, result scenarioruntime.ExecutionResult) error {
				state, err := scheduler.Inspect(ctx)
				if err != nil || len(state.Entries) != 1 || state.Entries[0].Status != "running" || !slices.Contains(*runtime.calls, "cleanup") {
					t.Error("capacity was not held through cleanup")
				}
				if !safe {
					return errors.New("PRIVATE_CAPACITY_SENTINEL")
				}
				return nil
			}}
			receipt, err := Execute(context.Background(), request, sourceFixture(t, request), host, sink)
			wanted := outcome
			if !safe && outcome == scenarioruntime.Passed {
				wanted = scenarioruntime.Error
			}
			if receipt.Result.Outcome != wanted || (err == nil) != (safe && outcome == scenarioruntime.Passed) {
				t.Fatal(receipt.Result, err)
			}
			state, _ := scheduler.Inspect(context.Background())
			if safe && len(state.Entries) != 0 || !safe && (len(state.Entries) != 1 || state.Entries[0].Status != "recovery_required") {
				t.Fatal(state)
			}
			var result scenarioruntime.ExecutionResult
			if json.Unmarshal(sink.files["result.json"], &result) != nil || result.Outcome != receipt.Result.Outcome || !slices.Contains(result.Artifacts, "scheduling.json") {
				t.Fatal("persisted result differs from receipt")
			}
			for _, data := range sink.files {
				if strings.Contains(string(data), "PRIVATE_CAPACITY_SENTINEL") {
					t.Fatal("capacity verifier leaked private details")
				}
			}
		}
	}
}

func TestCancelledCapacityWaitDoesNotExecuteOrLoseReceipt(t *testing.T) {
	scheduler := &scheduling.Scheduler{Store: &scheduling.MemoryStore{}, Pool: "kind/test", Capacity: 1, PollInterval: time.Millisecond}
	lease, _, _ := scheduler.Acquire(context.Background(), "active", "other-worker")
	defer lease.Finish(context.Background(), true)
	request, _, _ := requestFixture(t, Authoring{Definition: "Feature: One"})
	host, runtime, _, sink := hostFixture()
	host.Scheduling = &Scheduling{Scheduler: scheduler, Worker: "waiting-worker", Verify: func(context.Context, scenarioruntime.ExecutionResult) error { return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	receipt, err := Execute(ctx, request, sourceFixture(t, request), host, sink)
	if err == nil || receipt.Result.Outcome != scenarioruntime.Cancelled || slices.Contains(*runtime.calls, "execute") || len(sink.files["receipt.json"]) == 0 {
		t.Fatal(receipt, err)
	}
	state, _ := scheduler.Inspect(context.Background())
	if len(state.Entries) != 1 || state.Entries[0].Request != "active" {
		t.Fatal("cancelled wait changed active execution", state)
	}
}

func TestSchedulingEvidenceFailurePreventsExecutionAndReleasesUnusedSlot(t *testing.T) {
	request, _, _ := requestFixture(t, Authoring{Definition: "Feature: One"})
	host, runtime, _, sink := hostFixture()
	scheduler := &scheduling.Scheduler{Store: &scheduling.MemoryStore{}, Pool: "kind/test", Capacity: 1}
	host.Scheduling = &Scheduling{Scheduler: scheduler, Worker: "worker", Verify: func(context.Context, scenarioruntime.ExecutionResult) error {
		t.Error("verified an execution that never started")
		return nil
	}}
	sink.fail = "scheduling.json"
	_, err := Execute(context.Background(), request, sourceFixture(t, request), host, sink)
	state, _ := scheduler.Inspect(context.Background())
	if err == nil || slices.Contains(*runtime.calls, "execute") || len(state.Entries) != 0 {
		t.Fatal(state, err)
	}
}
