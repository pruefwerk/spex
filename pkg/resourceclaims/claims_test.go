package resourceclaims

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func coordinator() *Coordinator {
	return &Coordinator{Store: &MemoryStore{}, PollInterval: time.Millisecond}
}
func acquire(t *testing.T, c *Coordinator, claims ...Claim) *Session {
	t.Helper()
	session, _, err := c.Acquire(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestSharedReadersAndIndependentResourcesOverlap(t *testing.T) {
	c := coordinator()
	for _, claims := range [][]Claim{{{"aws/gateway/a", Shared}}, {{"aws/gateway/a", Shared}}, {{"aws/gateway/b", Exclusive}}} {
		session := acquire(t, c, claims...)
		defer session.Finish(context.Background(), true)
	}
	holdings, err := c.Inspect(context.Background())
	if err != nil || len(holdings) != 3 {
		t.Fatal(holdings, err)
	}
}

func TestExclusiveWaitsAndCancellationAcquiresNoSubset(t *testing.T) {
	c := coordinator()
	active := acquire(t, c, Claim{"aws/a", Exclusive})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	session, report, err := c.Acquire(ctx, []Claim{{"aws/b", Exclusive}, {"aws/a", Shared}})
	if session != nil || !errors.Is(err, context.DeadlineExceeded) || report.Status != "not_acquired" {
		t.Fatal(session, report, err)
	}
	holdings, _ := c.Inspect(context.Background())
	if len(holdings) != 1 {
		t.Fatal("partial acquisition", holdings)
	}
	independent := acquire(t, c, Claim{"aws/b", Exclusive})
	if err := active.Finish(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := independent.Finish(context.Background(), true); err != nil {
		t.Fatal(err)
	}
}

func TestWaitingWriterContinuesAfterReaderReleases(t *testing.T) {
	c := coordinator()
	reader := acquire(t, c, Claim{"aws/a", Shared})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan *Session, 1)
	errors := make(chan error, 1)
	go func() {
		session, _, err := c.Acquire(ctx, []Claim{{"aws/a", Exclusive}})
		errors <- err
		done <- session
	}()
	select {
	case <-done:
		t.Fatal("writer overlapped reader")
	case <-time.After(10 * time.Millisecond):
	}
	if err := reader.Finish(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	select {
	case session := <-done:
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		if err := session.Finish(context.Background(), true); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("writer never resumed")
	}
}

func TestRecoveryBlocksSuccessorsUntilVerified(t *testing.T) {
	c := coordinator()
	session := acquire(t, c, Claim{"aws/a", Exclusive})
	if err := session.Finish(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, _, err := c.Acquire(ctx, []Claim{{"aws/a", Shared}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := c.Recover(context.Background(), session.owner, Recovery{OwnerStopped: true, ResourcesSafe: true}); err == nil {
		t.Fatal("recovered without evidence")
	}
	cleared, err := c.Recover(context.Background(), session.owner, Recovery{true, true, "operator-check.json"})
	if err != nil || !cleared.RecoveryRequired {
		t.Fatal(cleared, err)
	}
	next := acquire(t, c, Claim{"aws/a", Exclusive})
	if err := session.Finish(context.Background(), true); err == nil {
		t.Fatal("old owner modified new holding")
	}
	next.Finish(context.Background(), true)
}

func TestCanonicalClaimsAndValidation(t *testing.T) {
	claims, err := Normalize([]Claim{{"b", Shared}, {"a", Shared}, {"a", Exclusive}})
	if err != nil || !reflect.DeepEqual(claims, []Claim{{"a", Exclusive}, {"b", Shared}}) {
		t.Fatal(claims, err)
	}
	for _, claim := range []Claim{{"", Shared}, {"line\nbreak", Shared}, {"a", "misspelled"}} {
		if _, err := Normalize([]Claim{claim}); err == nil {
			t.Fatal("accepted invalid claim")
		}
	}
}

type failingStore struct{}

func (failingStore) Transaction(context.Context, func(*State) error) error {
	return errors.New("SENTINEL_SECRET")
}
func TestBackendErrorsStayPrivateAndClaimsAreUncertain(t *testing.T) {
	c := &Coordinator{Store: failingStore{}}
	_, report, err := c.Acquire(context.Background(), []Claim{{"a", Exclusive}})
	if !errors.Is(err, ErrStore) || report.Status != "unknown" || report.Owner == "" {
		t.Fatal(report, err)
	}
}
