package spex

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sort"

	"github.com/pruefwerk/spex/pkg/scenario"
)

// Submission metadata is separate from semantic scenario and package identities.
type remoteRequest struct {
	Schema       string `json:"schema"`
	RequestID    string `json:"request_id"`
	ScenarioID   string `json:"scenario_id"`
	Runtime      string `json:"runtime"`
	PackageSHA   string `json:"package_sha256"`
	PackagePath  string `json:"package_path"`
	ScenarioPath string `json:"scenario_path"`
	RequestPath  string `json:"request_path"`
}

func packageScenario(document scenario.Scenario, root, output string, support []string, out io.Writer) error {
	doc, err := scenario.CanonicalizeRequest(document)
	if err != nil {
		return err
	}
	if err := scenario.ValidateSources(doc, root); err != nil {
		return err
	}
	canonical, err := scenario.SerializeRequest(doc)
	if err != nil {
		return err
	}
	files := map[string][]byte{"scenario.toml": canonical}
	paths := append([]string{}, support...)
	paths = append(paths, doc.Dependencies...)
	for _, test := range doc.Tests {
		if test.File != nil {
			paths = append(paths, *test.File)
		}
	}
	total := len(canonical)
	for _, relative := range paths {
		name, err := scenario.RelativePath(relative)
		if err != nil || name == "scenario.toml" {
			return errors.New("invalid or reserved package source path")
		}
		if _, exists := files[name]; exists {
			continue
		}
		if len(files) >= 1000 {
			return errors.New("too many package sources")
		}
		path, err := scenario.SourcePath(root, name)
		if err != nil {
			return errors.New("package source unavailable or outside workspace")
		}
		data, err := readRegularEvidenceFile(path, 16<<20)
		if err != nil {
			return errors.New("cannot read package source")
		}
		total += len(data)
		if total > 16<<20 {
			return errors.New("scenario package exceeds 16 MiB")
		}
		files[name] = data
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, name := range names {
		file, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return errors.New("cannot package scenario")
		}
		if _, err := file.Write(files[name]); err != nil {
			return errors.New("cannot package source")
		}
	}
	if err := archive.Close(); err != nil {
		return errors.New("cannot finish package")
	}
	id := hex.EncodeToString(sha256Sum(canonical))
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return errors.New("cannot generate request identity")
	}
	base := filepath.Dir(output)
	request := remoteRequest{Schema: "spex.submission/v1", RequestID: hex.EncodeToString(nonce), ScenarioID: id, Runtime: doc.Runtime, PackageSHA: hex.EncodeToString(sha256Sum(buffer.Bytes())), PackagePath: filepath.Join(base, "scenario-package.zip"), ScenarioPath: output, RequestPath: filepath.Join(base, "request.json")}
	if doc.Schema == scenario.RequestSchema {
		request.Schema = "spex.submission/v2"
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{output, canonical}, {request.PackagePath, buffer.Bytes()}} {
		if err := writeCanonicalScenario(root, file.name, file.data); err != nil {
			return err
		}
	}
	data, _ := json.Marshal(request)
	if err := writeCanonicalScenario(root, request.RequestPath, data); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(request)
}

func sha256Sum(data []byte) []byte { sum := sha256.Sum256(data); return sum[:] }
