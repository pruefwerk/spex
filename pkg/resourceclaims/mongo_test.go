package resourceclaims

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestStoredStateStrictDecoding(t *testing.T) {
	for _, data := range []string{"", "{", `{"unexpected":true}`, `{"holdings":[]} {}`, `{"holdings":[{"owner":"invalid","claims":[]}]}`, strings.Repeat(" ", (8<<20)+1)} {
		if _, err := decodeStoredState([]byte(data)); err == nil {
			t.Fatal("accepted invalid stored state")
		}
	}
	if _, err := decodeStoredState([]byte(`{"holdings":[]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMongoStore(nil, "test"); err == nil {
		t.Fatal("accepted missing collection")
	}
}

// Set SPEX_CLAIMS_MONGODB_URI to a disposable test server, never production.
// The test creates and drops only its own uniquely named database.
func TestMongoIndependentClientsCoordinate(t *testing.T) {
	uri := os.Getenv("SPEX_CLAIMS_MONGODB_URI")
	if uri == "" {
		t.Skip("set SPEX_CLAIMS_MONGODB_URI for shared-store integration qualification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	clients := make([]*mongo.Client, 2)
	for i := range clients {
		client, err := mongo.Connect(options.Client().ApplyURI(uri))
		if err != nil {
			t.Fatal("connect test server")
		}
		clients[i] = client
		t.Cleanup(func() { client.Disconnect(context.Background()) })
		if err := client.Ping(ctx, nil); err != nil {
			t.Fatal("test server unavailable")
		}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	database := "spex_claims_test_" + hex.EncodeToString(nonce[:])
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := clients[0].Database(database).Drop(cleanup); err != nil {
			t.Error("test database cleanup failed")
		}
	})
	coordinators := make([]*Coordinator, 2)
	for i, client := range clients {
		store, err := NewMongoStore(client.Database(database).Collection("claims"), "aws-dev")
		if err != nil {
			t.Fatal(err)
		}
		coordinators[i] = &Coordinator{Store: store, PollInterval: time.Millisecond}
	}
	claim := []Claim{{"aws-dev/gateway/a", Exclusive}}
	first, report, err := coordinators[0].Acquire(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinators[1].Inspect(ctx); err != nil {
		t.Fatal("independent client cannot inspect claims", err)
	}
	wait, stop := context.WithTimeout(ctx, 300*time.Millisecond)
	_, blocked, err := coordinators[1].Acquire(wait, claim)
	stop()
	// A deadline during a MongoDB read has an unknown backend disposition;
	// a deadline between conflict polls is known not to have acquired claims.
	if !errors.Is(err, context.DeadlineExceeded) || (blocked.Status != "not_acquired" && blocked.Status != "unknown") {
		t.Fatal("independent client did not wait for exclusive claim", err)
	}
	if holdings, err := coordinators[0].Inspect(ctx); err != nil || len(holdings) != 1 || holdings[0].Owner != report.Owner {
		t.Fatal("conflicting client acquired claims", err)
	}
	if err := first.Finish(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinators[1].Recover(ctx, report.Owner, Recovery{true, true, "local-integration-worker-stopped"}); err != nil {
		t.Fatal(err)
	}
	// Competing clients must not lose updates to unrelated resource holdings.
	var workers sync.WaitGroup
	acquired := make(chan struct{}, 12)
	release := make(chan struct{})
	for i := range 12 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			session, _, err := coordinators[i%2].Acquire(ctx, []Claim{{"aws-dev/gateway/" + string(rune('b'+i)), Exclusive}})
			if err != nil {
				t.Error(err)
				acquired <- struct{}{}
				return
			}
			acquired <- struct{}{}
			<-release
			if err := session.Finish(ctx, true); err != nil {
				t.Error(err)
			}
		}()
	}
	for range 12 {
		<-acquired
	}
	held, inspectErr := coordinators[1].Inspect(ctx)
	close(release)
	workers.Wait()
	if inspectErr != nil || len(held) != 12 {
		t.Fatal("concurrent holdings lost", inspectErr)
	}
	holdings, err := coordinators[0].Inspect(ctx)
	if err != nil || len(holdings) != 0 {
		t.Fatal("claims leaked or updates lost", err)
	}
}
