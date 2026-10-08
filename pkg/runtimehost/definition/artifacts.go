package definition

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode"
)

type ArtifactPin struct {
	Chart   string `json:"chart"`
	Version string `json:"version"`
	Image   string `json:"image"`
	Tag     string `json:"tag"`
}
type Artifacts struct {
	Registry string                 `json:"registry"`
	Services map[string]ArtifactPin `json:"services"`
}
type Artifact struct {
	Registry        string `json:"registry"`
	Chart           string `json:"chart"`
	Version         string `json:"version"`
	ImageRepository string `json:"image_repository"`
	Tag             string `json:"tag"`
	Image           string `json:"image"`
}

func (r *Resolver) ReadArtifacts(root string) (Artifacts, error) {
	var manifest Artifacts
	err := readDocument(filepath.Join(root, r.snapshot.ArtifactManifest), &manifest, true)
	return manifest, err
}

func (m Artifacts) Select(service string) (Artifact, error) {
	pin, ok := m.Services[service]
	if !ok {
		return Artifact{}, errors.New("unknown artifact service")
	}
	for _, value := range []string{m.Registry, pin.Chart, pin.Version, pin.Image, pin.Tag} {
		if value == "" || strings.IndexFunc(value, unicode.IsSpace) >= 0 {
			return Artifact{}, errors.New("artifact fields must be nonempty without whitespace")
		}
	}
	repo := m.Registry + "/" + pin.Image
	return Artifact{m.Registry, "oci://" + m.Registry + "/" + pin.Chart, pin.Version, repo, pin.Tag, repo + ":" + pin.Tag}, nil
}

// Fields retains the shell adapter's data-only order without generating code.
func (a Artifact) Fields() []string {
	return []string{a.Registry, a.Chart, a.Version, a.ImageRepository, a.Tag, a.Image}
}
