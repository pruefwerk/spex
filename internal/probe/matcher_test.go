package probe

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEvaluateMatchersSupportsPrimitiveExpectations(t *testing.T) {
	boolTrue := true
	nullTrue := true
	document := map[string]any{
		"name":    "meter-1",
		"value":   mustNumber(t, "42.50"),
		"enabled": true,
		"missing": nil,
		"items": []any{
			map[string]any{"id": "first"},
			map[string]any{"id": "second"},
		},
	}
	matchers := []Matcher{
		{Path: "$.name", EqualsString: "meter-1"},
		{Path: "$.value", EqualsNumber: "42.5"},
		{Path: "$.enabled", EqualsBool: &boolTrue},
		{Path: "$.missing", EqualsNull: &nullTrue},
		{Path: "$.items[1].id", EqualsString: "second"},
	}

	if err := EvaluateMatchers(matchers, document); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateMatchersRejectsTypeCoercion(t *testing.T) {
	err := EvaluateMatchers([]Matcher{{Path: "$.value", EqualsNumber: "42.5"}}, map[string]any{"value": "42.5"})
	if err == nil || !strings.Contains(err.Error(), "expected number") {
		t.Fatalf("expected number type error, got %v", err)
	}
}

func TestEvaluateMatchersSupportsTimeNotOlderThan(t *testing.T) {
	document := map[string]any{
		"timestamp": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
	}
	matchers := []Matcher{
		{Path: "$.timestamp", TimeNotOlderThan: "5m"},
	}

	if err := EvaluateMatchers(matchers, document); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateMatchersRejectsStaleTimestamp(t *testing.T) {
	document := map[string]any{
		"timestamp": time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano),
	}

	err := EvaluateMatchers([]Matcher{{Path: "$.timestamp", TimeNotOlderThan: "5m"}}, document)
	if err == nil || !strings.Contains(err.Error(), "older than 5m0s") {
		t.Fatalf("expected stale timestamp error, got %v", err)
	}
}

func TestEvaluateMatchersReportsMissingPath(t *testing.T) {
	err := EvaluateMatchers([]Matcher{{Path: "$.missing", EqualsString: "x"}}, map[string]any{"value": "x"})
	if err == nil || !strings.Contains(err.Error(), "missing key") {
		t.Fatalf("expected missing key error, got %v", err)
	}
}

func mustNumber(t *testing.T, raw string) any {
	t.Helper()
	document, err := loadJSONFromString(`{"value":` + raw + `}`)
	if err != nil {
		t.Fatal(err)
	}
	return document.(map[string]any)["value"]
}

func loadJSONFromString(raw string) (any, error) {
	var document any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	return document, nil
}
