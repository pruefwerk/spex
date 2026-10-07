//go:build linux || darwin

package resourceclaims

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreProcessHelper(t *testing.T) {
	if os.Getenv("SPEX_CLAIM_HELPER") == "" {
		return
	}
	store, err := NewFileStore(os.Getenv("SPEX_CLAIM_DIRECTORY"))
	if err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{Store: store}
	if _, _, err := c.Acquire(context.Background(), []Claim{{"aws/a", Exclusive}}); err != nil {
		t.Fatal(err)
	}
	// Exit without release to simulate a lost worker, not a gracefully completed run.
}

func TestDurableClaimsSurviveProcessExitAndRequireRecovery(t *testing.T) {
	directory := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestFileStoreProcessHelper$")
	command.Env = append(os.Environ(), "SPEX_CLAIM_HELPER=1", "SPEX_CLAIM_DIRECTORY="+directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v %s", err, output)
	}
	store, _ := NewFileStore(directory)
	c := &Coordinator{Store: store, PollInterval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, _, err := c.Acquire(ctx, []Claim{{"aws/a", Shared}}); err == nil {
		t.Fatal("expired lost worker implicitly")
	}
	holdings, err := c.Inspect(context.Background())
	if err != nil || len(holdings) != 1 {
		t.Fatal(holdings, err)
	}
	if _, err := c.Recover(context.Background(), holdings[0].Owner, Recovery{true, true, "verified-owner-exit.json"}); err != nil {
		t.Fatal(err)
	}
	session := acquire(t, c, Claim{"aws/a", Exclusive})
	session.Finish(context.Background(), true)
}

func TestCorruptFileStoreFailsClosed(t *testing.T) {
	directory := t.TempDir()
	store, _ := NewFileStore(directory)
	if err := os.WriteFile(filepath.Join(directory, "claims.json"), []byte("truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{Store: store}
	if _, _, err := c.Acquire(context.Background(), []Claim{{"a", Exclusive}}); err == nil {
		t.Fatal("ignored corrupt coordination state")
	}
}
