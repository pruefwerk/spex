// Package receiver implements the reusable, in-process side of spex-action's
// transport contract. Hosting workflows own authentication, source checkout,
// durable idempotency, credentials, runtime implementations and infrastructure.
package receiver

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
)

const MaxRequestBytes = 4 << 20
const MaxReceiptBytes = 2 << 20

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var shaPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var requestPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

type Source struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	Root       string `json:"root"`
	RunID      int64  `json:"run_id"`
}

type Authoring struct {
	Definition      string   `json:"definition"`
	DefinitionFiles []string `json:"definition_files"`
	Runtime         string   `json:"runtime"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Timeout         string   `json:"timeout"`
}

type Envelope struct {
	Schema    string    `json:"schema"`
	RequestID string    `json:"request_id"`
	Source    Source    `json:"source"`
	Authoring Authoring `json:"authoring"`
}

// Expected must come from authenticated dispatch/artifact/run metadata, not
// from the request being checked. Source root remains caller metadata.
type Expected struct {
	RequestID, SHA256, Repository, Commit string
	SourceRunID                           int64
}

// Request stores verified input privately. Accessors cannot mutate it.
type Request struct {
	envelope Envelope
	digest   string
}

func (r Request) Envelope() Envelope {
	value := r.envelope
	value.Authoring.DefinitionFiles = append([]string(nil), value.Authoring.DefinitionFiles...)
	return value
}
func (r Request) SHA256() string { return r.digest }

func Decode(data []byte, expected Expected) (Request, error) {
	bad := errors.New("invalid receiver request or identity")
	if len(data) > MaxRequestBytes || !requestPattern.MatchString(expected.RequestID) || !digestPattern.MatchString(expected.SHA256) || !repoPattern.MatchString(expected.Repository) || !shaPattern.MatchString(expected.Commit) || expected.SourceRunID <= 0 {
		return Request{}, bad
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != expected.SHA256 {
		return Request{}, bad
	}
	if err := uniqueJSON(data); err != nil {
		return Request{}, bad
	}
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Request{}, bad
	}
	if envelope.Schema != "spex.transport/v1" || envelope.RequestID != expected.RequestID || envelope.Source.Repository != expected.Repository || envelope.Source.Commit != expected.Commit || envelope.Source.RunID != expected.SourceRunID {
		return Request{}, bad
	}
	root := envelope.Source.Root
	if root == "" || path.IsAbs(root) || strings.ContainsAny(root, "\\\x00\r\n:") {
		return Request{}, bad
	}
	for _, segment := range strings.Split(root, "/") {
		if segment == ".." {
			return Request{}, bad
		}
	}
	if envelope.Authoring.Definition != "" && len(envelope.Authoring.DefinitionFiles) != 0 {
		return Request{}, errors.New("inline and file definitions cannot be combined")
	}
	return Request{envelope: envelope, digest: expected.SHA256}, nil
}

// DecodeArtifact reads the downloaded GitHub ZIP without extracting any paths.
func DecodeArtifact(data []byte, expected Expected) (Request, error) {
	bad := errors.New("invalid receiver request artifact")
	if len(data) > MaxRequestBytes+(1<<20) {
		return Request{}, bad
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) != 1 {
		return Request{}, bad
	}
	file := archive.File[0]
	if file.Name != "request.json" || !file.Mode().IsRegular() || file.UncompressedSize64 > MaxRequestBytes {
		return Request{}, bad
	}
	stream, err := file.Open()
	if err != nil {
		return Request{}, bad
	}
	defer stream.Close()
	content, err := io.ReadAll(io.LimitReader(stream, MaxRequestBytes+1))
	if err != nil {
		return Request{}, bad
	}
	return Decode(content, expected)
}

// JSON duplicate keys and null fields are ambiguous authoring inputs. Refuse
// them, excessive nesting and extra documents before decoding typed fields.
func uniqueJSON(data []byte) error {
	return checkUniqueJSON(data, false)
}

func checkUniqueJSON(data []byte, allowNull bool) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var value func(int) error
	value = func(depth int) error {
		if depth > 16 {
			return errors.New("JSON nesting limit")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		if token == nil && !allowNull {
			return errors.New("null field")
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate JSON key")
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("extra JSON content")
	}
	return nil
}
