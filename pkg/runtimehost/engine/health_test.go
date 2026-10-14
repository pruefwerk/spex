package engine

import (
	"bytes"
	"strings"
	"testing"
)

func TestResourceHealthExplainsMissingAndUnreadyWorkloads(t *testing.T) {
	h := &Host{}
	h.config.HealthResources = []string{"service", "database"}
	for _, tt := range []struct {
		name, data, want string
		healthy          bool
	}{
		{"empty", `{"items":[]}`, "required workload database is missing", false},
		{"unready", `{"items":[{"metadata":{"name":"service"},"spec":{"replicas":2},"status":{"readyReplicas":1}},{"metadata":{"name":"database"},"status":{"readyReplicas":1}}]}`, "service has 1/2 ready replicas", false},
		{"ready", `{"items":[{"metadata":{"name":"service"},"status":{"readyReplicas":1}},{"metadata":{"name":"database"},"status":{"readyReplicas":1}}]}`, "required workloads are ready", true},
		{"private malformed response", "SECRET_BACKEND", "invalid workload response", false},
		{"private workload name", `{"items":[{"metadata":{"name":"SECRET_BACKEND"},"spec":{"replicas":1}}]}`, "1 unready workloads", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if got := h.checkResourceHealth(tt.data, &out); got != tt.healthy || !strings.Contains(out.String(), tt.want) || strings.Contains(out.String(), "SECRET_BACKEND") {
				t.Fatalf("healthy=%t output=%q", got, out.String())
			}
		})
	}
}
