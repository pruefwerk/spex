package scheduling

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Point SPEX_SCHEDULING_TEST_MONGODB_URI only at a disposable test server. The
// test creates and drops its own uniquely named database, never a runtime pool.
func TestMongoIndependentWorkersShareDurableCapacity(t *testing.T) {
	uri := os.Getenv("SPEX_SCHEDULING_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("set disposable SPEX_SCHEDULING_TEST_MONGODB_URI for live qualification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	database := "spex_scheduling_test_" + hex.EncodeToString(nonce[:])
	clients := make([]*mongo.Client, 2)
	schedulers := make([]*Scheduler, 2)
	for i := range clients {
		client, err := mongo.Connect(options.Client().ApplyURI(uri))
		if err != nil {
			t.Fatal("connect test server")
		}
		clients[i] = client
		t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
		if client.Ping(ctx, nil) != nil {
			t.Fatal("test server unavailable")
		}
		store, err := NewMongoStore(client.Database(database).Collection("queue"), "kind/test")
		if err != nil {
			t.Fatal(err)
		}
		schedulers[i] = &Scheduler{Store: store, Pool: "kind/test", Capacity: 2, PollInterval: time.Millisecond}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if clients[0].Database(database).Drop(ctx) != nil {
			t.Error("test database cleanup failed")
		}
	})
	var active atomic.Int64
	var workers sync.WaitGroup
	for i := range 20 {
		workers.Go(func() {
			lease, _, err := schedulers[i%2].Acquire(ctx, fmt.Sprint(i), fmt.Sprint("worker-", i))
			if err != nil {
				t.Error(err)
				return
			}
			if active.Add(1) > 2 {
				t.Error("independent clients exceeded capacity")
			}
			time.Sleep(2 * time.Millisecond)
			active.Add(-1)
			if lease.Finish(ctx, true) != nil {
				t.Error("capacity release failed")
			}
		})
	}
	workers.Wait()
	state, err := schedulers[1].Inspect(ctx)
	if err != nil || len(state.Entries) != 0 || state.Next != 20 {
		t.Fatal("durable queue is inconsistent", err)
	}
	lease, report, err := schedulers[0].Acquire(ctx, "crashed", "stopped-worker")
	if err != nil {
		t.Fatal(err)
	}
	lease, err = schedulers[1].Resume(ctx, report)
	if err != nil || lease.Finish(ctx, false) != nil {
		t.Fatal("could not retain failed worker", err)
	}
	state, err = schedulers[1].Inspect(ctx)
	if err != nil || len(state.Entries) != 1 || state.Entries[0].Status != "recovery_required" {
		t.Fatal("another client lost recovery state", err)
	}
	if _, err := schedulers[1].Recover(ctx, report.Owner, Recovery{true, true, "disposable-integration-proof"}); err != nil {
		t.Fatal("verified recovery failed", err)
	}
}
