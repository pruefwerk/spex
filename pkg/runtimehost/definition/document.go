package definition

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// Reject ambiguous JSON before typed decoding. Configuration is never merged
// through the temporary token walk; each concrete model remains authoritative.
func readDocument(path string, destination any, strict bool) error {
	file, err := os.Open(path)
	if err != nil {
		return errors.New("configuration document unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return errors.New("configuration document exceeds limit")
	}
	check := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSON(check); err != nil {
		return err
	}
	if _, err := check.Token(); err != io.EOF {
		return errors.New("invalid configuration document")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(destination); err != nil {
		return errors.New("invalid configuration fields")
	}
	return nil
}

func uniqueJSON(decoder *json.Decoder) error {
	return walkJSON(decoder, 0)
}

func walkJSON(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("configuration JSON nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return errors.New("invalid configuration JSON")
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	if delim == '{' {
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return errors.New("duplicate or invalid configuration key")
			}
			seen[name] = true
			if err := walkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	} else if delim == '[' {
		for decoder.More() {
			if err := walkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	} else {
		return errors.New("invalid configuration JSON")
	}
	_, err = decoder.Token()
	return err
}
