package scenarioruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type memoryArtifacts struct {
	files map[string][]byte
	fail  string
}

func (m *memoryArtifacts) Directory() string { return "" }
func (m *memoryArtifacts) Write(name string, data []byte) error {
	if name == m.fail {
		return errors.New("SENTINEL_SECRET")
	}
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[name] = append([]byte(nil), data...)
	return nil
}

func TestRunArtifactsAndFailureOrdering(t *testing.T) {
	for _, fail := range []string{"", "plan.json", "result.json"} {
		t.Run(fail, func(t *testing.T) {
			f := &fake{result: ExecutionResult{Outcome: Failed, Cleanup: "failed"}, executeErr: errors.New("SENTINEL_SECRET")}
			r := NewRegistry()
			_ = r.Register(f)
			p, err := r.Prepare(context.Background(), request())
			if err != nil {
				t.Fatal(err)
			}
			sink := &memoryArtifacts{fail: fail}
			result, err := Run(context.Background(), p, sink)
			if err == nil || strings.Contains(err.Error(), "SENTINEL_SECRET") {
				t.Fatal("missing or unsafe error")
			}
			if fail == "plan.json" {
				if len(f.calls) != 2 {
					t.Fatal("execution started before artifact preparation succeeded")
				}
			} else if result.Outcome != Failed {
				t.Fatal("reporting erased primary failure")
			}
			if fail == "result.json" && len(result.Problems) == 0 {
				t.Fatal("lost secondary report error")
			}
			for _, data := range sink.files {
				if strings.Contains(string(data), "SENTINEL_SECRET") {
					t.Fatal("secret escaped into artifacts")
				}
			}
		})
	}
}

func TestFileArtifactsConfinementAndReruns(t *testing.T) {
	root := t.TempDir()
	id := strings.Repeat("a", 64)
	a, err := NewFileArtifacts(root, ".spex/runs", id)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewFileArtifacts(root, ".spex/runs", id)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a.Directory() == b.Directory() {
		t.Fatal("rerun overwrites prior execution")
	}
	if err := a.Write("result.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := a.Write("result.json", []byte("overwrite")); err == nil {
		t.Fatal("overwrote existing artifact")
	}
	if err := a.Write("../escape", nil); err == nil {
		t.Fatal("escaped artifact root")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileArtifacts(root, "escape/runs", id); err == nil {
		t.Fatal("followed external symlink")
	}
}
