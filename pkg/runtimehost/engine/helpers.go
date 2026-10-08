package engine

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pruefwerk/spex/pkg/ownedkind"
)

type readyPod struct {
	Metadata struct {
		Name              string
		DeletionTimestamp *string
	}
	Status struct {
		Conditions []struct{ Type, Status string }
	}
}

func readyPods(data string, replicas int) ([]readyPod, bool) {
	var pods struct{ Items []readyPod }
	if json.Unmarshal([]byte(data), &pods) != nil || replicas < 1 || len(pods.Items) != replicas {
		return nil, false
	}
	for _, pod := range pods.Items {
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				ready = true
			}
		}
		if !ready || pod.Metadata.DeletionTimestamp != nil || pod.Metadata.Name == "" {
			return nil, false
		}
	}
	return pods.Items, true
}
func (h *Host) listenersStarted(log string) bool {
	policy := h.config.Readiness
	sources := map[string]bool{}
	for _, line := range strings.Split(log, "\n") {
		var event struct{ Message, Source string }
		if json.Unmarshal([]byte(line), &event) == nil && event.Message == policy.Message {
			sources[event.Source] = true
		}
	}
	for _, source := range policy.Sources {
		if !sources[source] {
			return false
		}
	}
	return len(policy.Sources) > 0
}
func (h *Host) waitListeners(ctx context.Context, config string, replicas int, runner ownedkind.Runner) error {
	policy := h.config.Readiness
	if policy.Namespace == "" || len(policy.Sources) == 0 {
		return errors.New("log readiness not configured")
	}
	command := []string{"kubectl", "--kubeconfig", config, "--request-timeout=5s", "-n", policy.Namespace}
	run := func(args ...string) (string, error) {
		call, cancel := context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
		return runner.Run(call, append(append([]string{}, command...), args...)...)
	}
	for {
		if ctx.Err() != nil {
			return errors.New("log readiness cancelled or timed out")
		}
		data, err := run("get", "pods", "-l", policy.Selector, "-o", "json")
		pods, ready := readyPods(data, replicas)
		if err == nil && ready {
			for _, pod := range pods {
				log, err := run("logs", pod.Metadata.Name, "-c", policy.Container)
				if err != nil || !h.listenersStarted(log) {
					ready = false
					break
				}
			}
			if ready {
				return nil
			}
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("log readiness cancelled or timed out")
		case <-timer.C:
		}
	}
}
func recordString(row json.RawMessage, fields []string) (string, bool, error) {
	for _, field := range fields {
		if string(row) == "null" {
			return "", false, nil
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(row, &object) != nil || object == nil {
			return "", false, errors.New("invalid relation record")
		}
		var ok bool
		row, ok = object[field]
		if !ok {
			return "", false, nil
		}
	}
	if string(row) == "null" {
		return "", false, nil
	}
	var value string
	if json.Unmarshal(row, &value) != nil {
		return "", false, errors.New("relation field must contain a string")
	}
	return value, true, nil
}
func (h *Host) auditRelations(input io.Reader) ([]map[string]string, error) {
	var rows []json.RawMessage
	decoder := json.NewDecoder(io.LimitReader(input, 32<<20))
	if decoder.Decode(&rows) != nil || rows == nil {
		return nil, errors.New("expected an inventory JSON array")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("invalid inventory export")
	}
	policy := h.config.Audit
	type record struct{ id, parent, value string }
	inventory := map[string]record{}
	ordered := []record{}
	for _, row := range rows {
		id, present, err := recordString(row, policy.ID)
		if err != nil || !present {
			return nil, errors.New("inventory ID required")
		}
		if _, exists := inventory[id]; exists {
			return nil, errors.New("duplicate inventory ID")
		}
		parent, _, err := recordString(row, policy.Parent)
		if err != nil {
			return nil, err
		}
		value, _, err := recordString(row, policy.Value)
		if err != nil {
			return nil, err
		}
		entry := record{id, parent, value}
		inventory[id] = entry
		ordered = append(ordered, entry)
	}
	findings := []map[string]string{}
	for _, row := range ordered {
		if row.parent == "" {
			continue
		}
		parent, exists := inventory[row.parent]
		finding := map[string]string{policy.OutputID: row.id, policy.OutputParent: row.parent}
		if !exists {
			finding["condition"] = policy.MissingCondition
		} else if row.value != "" && parent.value != "" {
			finding["condition"] = policy.ConflictCondition
			finding[policy.OutputValue] = row.value
			finding[policy.OutputParentValue] = parent.value
		} else {
			continue
		}
		findings = append(findings, finding)
	}
	return findings, nil
}
func (h *Host) helperCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("helper operation required")
	}
	if args[0] == "audit-relations" || args[0] == "audit-bundles" {
		if len(args) != 2 {
			return errors.New("inventory export file required")
		}
		file, err := os.Open(args[1])
		if err != nil {
			return errors.New("inventory export unavailable")
		}
		defer file.Close()
		findings, err := h.auditRelations(file)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(struct {
			Findings []map[string]string `json:"findings"`
		}{findings})
	}
	if args[0] != "logs-ready" && args[0] != "mqtt-ready" {
		return errors.New("unknown configured helper")
	}
	policy := h.config.Readiness
	flags := flag.NewFlagSet("logs-ready", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := flags.String("kubeconfig", "", "private kubeconfig")
	replicas := flags.Int("replicas", policy.Replicas, "expected replicas")
	seconds := flags.Int("timeout", policy.TimeoutSeconds, "readiness timeout in seconds")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *config == "" || *replicas < 1 || *seconds < 1 {
		return errors.New("invalid log readiness arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(*seconds)*time.Second)
	defer cancel()
	if err := h.waitListeners(ctx, *config, *replicas, ownedkind.CommandRunner{}); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "Configured listeners started on all %d replicas\n", *replicas)
	return err
}
