package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/pruefwerk/spex/pkg/ownedkind"
)

// Select the existing runtime-owned pins and mirror aliases before any image
// operation. The generic library knows neither this inventory nor its defaults.
func (h *Host) kindImages(root, name string) ([]ownedkind.Image, error) {
	policy := h.config
	if len(policy.Images) == 0 && len(policy.ImageServices) == 0 && policy.MirrorInventory == "" {
		return []ownedkind.Image{}, nil
	}
	manifest, err := h.resolver.ReadArtifacts(root)
	if err != nil {
		return nil, err
	}

	base := manifest.Registry + "/" + policy.MirrorPrefix
	binding, err := readYAML(filepath.Join(root, "bindings", name+".yaml"))
	if err != nil {
		return nil, err
	}
	probe, err := scalarAt(binding, "spec", "probe", "image")
	if err != nil {
		return nil, err
	}
	images := []ownedkind.Image{}
	for _, configured := range policy.Images {
		alias := configured.Alias
		if configured.ProbeAlias {
			alias = probe.Value
		}
		build := &ownedkind.Build{Context: filepath.Join(root, configured.Build.Context), Dockerfile: filepath.Join(root, configured.Build.Dockerfile), Arguments: map[string]string{}, Contexts: map[string]string{}}
		for key, value := range configured.Build.Arguments {
			build.Arguments[key] = value
		}
		for key, value := range configured.Build.MirrorArguments {
			build.Arguments[key] = base + value
		}
		for key, value := range configured.Build.Contexts {
			build.Contexts[key] = filepath.Join(root, value)
		}
		images = append(images, ownedkind.Image{Alias: alias, Build: build})
	}
	for _, service := range policy.ImageServices {
		pin, err := manifest.Select(service)
		if err != nil {
			return nil, err
		}
		images = append(images, ownedkind.Image{Alias: pin.Image, Source: pin.Image})
	}
	var data []byte
	if policy.MirrorInventory != "" {
		data, err = os.ReadFile(filepath.Join(root, policy.MirrorInventory))
		if err != nil || len(data) > 4<<20 {
			return nil, errors.New("mirror inventory unavailable or oversized")
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			return nil, errors.New("mirror inventory requires five fields")
		}
		if fields[4] != "-" {
			images = append(images, ownedkind.Image{Alias: fields[4], Source: manifest.Registry + "/" + fields[2] + ":" + fields[3]})
		}
	}
	seen := map[string]bool{}
	for _, image := range images {
		if image.Alias == "" || strings.ContainsAny(image.Alias, " \t\r\n") || strings.HasPrefix(image.Alias, "-") || seen[image.Alias] {
			return nil, errors.New("invalid or duplicate image alias")
		}
		seen[image.Alias] = true
		if image.Source != "" && (strings.ContainsAny(image.Source, " \t\r\n") || strings.HasPrefix(image.Source, "-")) {
			return nil, errors.New("invalid source image")
		}
	}
	return images, nil
}

func (h *Host) prepareKindImages(ctx context.Context, root, scopePath string, runner ownedkind.Runner) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	cluster, err := h.nativeKind(root, scopePath, runner)
	if err != nil {
		return err
	}
	scope, err := h.resolver.LoadScope(scopePath)
	if err != nil {
		return err
	}
	name, _ := scope.Name()
	nodes, err := cluster.OwnedNodes(ctx)
	if err != nil || len(nodes) == 0 {
		return errors.New("no owned Kind nodes")
	}
	images, err := h.kindImages(root, name)
	if err != nil {
		return err
	}
	fingerprint, err := ownedkind.Fingerprint(root, images, h.config.ImageInputs)
	if err != nil {
		return err
	}
	ready := filepath.Join(root, ".spex/isolated", name, "images-ready.json")
	if _, err := os.Lstat(ready); err == nil {
		var receipt struct {
			Scope       string `json:"scope"`
			Fingerprint string `json:"fingerprint"`
		}
		if readJSON(ready, &receipt) != nil || receipt.Scope != name || receipt.Fingerprint != fingerprint {
			return errors.New("image preparation inputs changed or receipt invalid; allocate a fresh execution")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, image := range images {
		if err := cluster.LoadImage(ctx, image); err != nil {
			return err
		}
	}
	data, _ := json.Marshal(struct {
		Scope       string `json:"scope"`
		Fingerprint string `json:"fingerprint"`
	}{name, fingerprint})
	return writePrivate(ready, data)
}
