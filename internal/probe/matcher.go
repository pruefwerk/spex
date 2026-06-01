package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
)

type Matcher struct {
	Path         string `json:"path"`
	EqualsString string `json:"equalsString,omitempty"`
	EqualsNumber string `json:"equalsNumber,omitempty"`
	EqualsBool   *bool  `json:"equalsBool,omitempty"`
	EqualsNull   *bool  `json:"equalsNull,omitempty"`
}

func EvaluateMatchersFile(matchersPath, documentPath string) error {
	matchers, err := loadMatchers(matchersPath)
	if err != nil {
		return err
	}
	document, err := loadJSON(documentPath)
	if err != nil {
		return err
	}
	return EvaluateMatchers(matchers, document)
}

func EvaluateMatchersFileAgainstDocument(matchersPath string, document any) error {
	matchers, err := loadMatchers(matchersPath)
	if err != nil {
		return err
	}
	return EvaluateMatchers(matchers, document)
}

func EvaluateMatchersBytes(matchersPath string, documentBytes []byte) error {
	matchers, err := loadMatchers(matchersPath)
	if err != nil {
		return err
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(documentBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("document: %w", err)
	}
	return EvaluateMatchers(matchers, document)
}

func loadMatchers(path string) ([]Matcher, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var matchers []Matcher
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&matchers); err != nil {
		return nil, fmt.Errorf("matchers: %w", err)
	}
	for i, matcher := range matchers {
		if matcher.Path == "" {
			return nil, fmt.Errorf("matcher[%d] path is required", i)
		}
		expectationCount := 0
		if matcher.EqualsString != "" {
			expectationCount++
		}
		if matcher.EqualsNumber != "" {
			expectationCount++
		}
		if matcher.EqualsBool != nil {
			expectationCount++
		}
		if matcher.EqualsNull != nil {
			expectationCount++
		}
		if expectationCount != 1 {
			return nil, fmt.Errorf("matcher[%d] must specify exactly one expectation", i)
		}
	}
	return matchers, nil
}

func loadJSON(path string) (any, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("document: %w", err)
	}
	return document, nil
}

func EvaluateMatchers(matchers []Matcher, document any) error {
	for i, matcher := range matchers {
		value, err := lookupPath(document, matcher.Path)
		if err != nil {
			return fmt.Errorf("matcher[%d] %s: %w", i, matcher.Path, err)
		}
		if err := evaluateMatcher(matcher, value); err != nil {
			return fmt.Errorf("matcher[%d] %s: %w", i, matcher.Path, err)
		}
	}
	return nil
}

func evaluateMatcher(matcher Matcher, value any) error {
	if matcher.EqualsString != "" {
		actual, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected string %q, got %T", matcher.EqualsString, value)
		}
		if actual != matcher.EqualsString {
			return fmt.Errorf("expected string %q, got %q", matcher.EqualsString, actual)
		}
		return nil
	}
	if matcher.EqualsNumber != "" {
		actual, ok := value.(json.Number)
		if !ok {
			return fmt.Errorf("expected number %s, got %T", matcher.EqualsNumber, value)
		}
		if !sameNumber(actual.String(), matcher.EqualsNumber) {
			return fmt.Errorf("expected number %s, got %s", matcher.EqualsNumber, actual.String())
		}
		return nil
	}
	if matcher.EqualsBool != nil {
		actual, ok := value.(bool)
		if !ok {
			return fmt.Errorf("expected bool %t, got %T", *matcher.EqualsBool, value)
		}
		if actual != *matcher.EqualsBool {
			return fmt.Errorf("expected bool %t, got %t", *matcher.EqualsBool, actual)
		}
		return nil
	}
	if matcher.EqualsNull != nil {
		if !*matcher.EqualsNull {
			return fmt.Errorf("equalsNull must be true when specified")
		}
		if value != nil {
			return fmt.Errorf("expected null, got %T", value)
		}
		return nil
	}
	return fmt.Errorf("missing expectation")
}

func sameNumber(actual, expected string) bool {
	actualRat, ok := new(big.Rat).SetString(actual)
	if !ok {
		return false
	}
	expectedRat, ok := new(big.Rat).SetString(expected)
	if !ok {
		return false
	}
	return actualRat.Cmp(expectedRat) == 0
}

func lookupPath(document any, path string) (any, error) {
	if path == "$" {
		return document, nil
	}
	if !strings.HasPrefix(path, "$.") {
		return nil, fmt.Errorf("unsupported path syntax")
	}
	current := document
	for _, segment := range strings.Split(strings.TrimPrefix(path, "$."), ".") {
		if segment == "" {
			return nil, fmt.Errorf("empty path segment")
		}
		name, indexes, err := parseSegment(segment)
		if err != nil {
			return nil, err
		}
		if name != "" {
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("segment %q needs object, got %T", name, current)
			}
			value, ok := object[name]
			if !ok {
				return nil, fmt.Errorf("missing key %q", name)
			}
			current = value
		}
		for _, index := range indexes {
			array, ok := current.([]any)
			if !ok {
				return nil, fmt.Errorf("index %d needs array, got %T", index, current)
			}
			if index < 0 || index >= len(array) {
				return nil, fmt.Errorf("index %d out of range", index)
			}
			current = array[index]
		}
	}
	return current, nil
}

func parseSegment(segment string) (string, []int, error) {
	nameEnd := strings.IndexByte(segment, '[')
	if nameEnd == -1 {
		return segment, nil, nil
	}
	name := segment[:nameEnd]
	rest := segment[nameEnd:]
	var indexes []int
	for rest != "" {
		if !strings.HasPrefix(rest, "[") {
			return "", nil, fmt.Errorf("unsupported segment %q", segment)
		}
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return "", nil, fmt.Errorf("unterminated index in %q", segment)
		}
		raw := rest[1:end]
		index, err := strconv.Atoi(raw)
		if err != nil {
			return "", nil, fmt.Errorf("invalid index %q", raw)
		}
		indexes = append(indexes, index)
		rest = rest[end+1:]
	}
	return name, indexes, nil
}
