package ownedkind

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type Build struct {
	Dockerfile string
	Context    string
	Arguments  map[string]string
	Contexts   map[string]string
}
type Image struct {
	Alias  string
	Source string
	Build  *Build
}
type imageAttempt struct {
	Tag   string `json:"tag"`
	Alias string `json:"alias"`
}
type imageRecord struct {
	Tag string `json:"tag"`
	ID  string `json:"id"`
}

var imageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var imageKey = regexp.MustCompile(`^[a-f0-9]{16}$`)

func (c *Cluster) imageTag(alias string) (string, string) {
	hash := sha256.Sum256([]byte(alias))
	key := hex.EncodeToString(hash[:])[:16]
	return c.config.ImageRepository + ":" + c.config.Name + "-" + key, key
}

func NormalizedImage(ref string) string {
	first, _, _ := strings.Cut(ref, "/")
	if !strings.Contains(ref, "/") {
		return "docker.io/library/" + ref
	}
	if !strings.ContainsAny(first, ".:") && first != "localhost" {
		return "docker.io/" + ref
	}
	return ref
}

func (c *Cluster) LoadImage(ctx context.Context, image Image) error {
	if image.Alias == "" || strings.IndexFunc(image.Alias, unicode.IsSpace) >= 0 || (image.Build == nil) == (image.Source == "") {
		return errors.New("image requires an alias and exactly one source or build")
	}
	if image.Build != nil && (!filepath.IsAbs(image.Build.Context) || !filepath.IsAbs(image.Build.Dockerfile)) {
		return errors.New("image build requires explicit context and Dockerfile paths")
	}
	if image.Source != "" && (strings.HasPrefix(image.Source, "-") || strings.IndexFunc(image.Source, unicode.IsSpace) >= 0) {
		return errors.New("invalid source image reference")
	}
	nodes, err := c.OwnedNodes(ctx)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return errors.New("execution has no live owned nodes")
	}
	tag, key := c.imageTag(image.Alias)
	if _, err := os.Lstat(filepath.Join(c.config.StateDirectory, "image-"+key+".json")); !os.IsNotExist(err) {
		return errors.New("image preparation already attempted; allocate a new execution after failure")
	}
	present, err := c.call(ctx, "docker", "image", "ls", "--quiet", tag)
	if err != nil {
		return err
	}
	if present != "" {
		return errors.New("private image tag already exists; refusing replacement")
	}
	if err := writeNew(filepath.Join(c.config.StateDirectory, "image-"+key+".json"), imageAttempt{tag, image.Alias}); err != nil {
		return err
	}
	if image.Build != nil {
		build := image.Build
		if build.Context == "" || build.Dockerfile == "" {
			return errors.New("image build requires a context and Dockerfile")
		}
		args := []string{"docker", "build", "-t", tag}
		for _, pair := range []struct {
			flag   string
			values map[string]string
		}{{"--build-arg", build.Arguments}, {"--build-context", build.Contexts}} {
			keys := make([]string, 0, len(pair.values))
			for key := range pair.values {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				args = append(args, pair.flag, key+"="+pair.values[key])
			}
		}
		args = append(args, "-f", build.Dockerfile, build.Context)
		if _, err := c.call(ctx, args...); err != nil {
			return err
		}
	} else {
		if _, err := c.call(ctx, "docker", "pull", image.Source); err != nil {
			return err
		}
		id, err := c.call(ctx, "docker", "image", "inspect", image.Source, "--format", "{{.Id}}")
		if err != nil {
			return err
		}
		if !imageID.MatchString(id) {
			return errors.New("invalid source image identity")
		}
		if _, err := c.call(ctx, "docker", "tag", id, tag); err != nil {
			return err
		}
	}
	id, err := c.call(ctx, "docker", "image", "inspect", tag, "--format", "{{.Id}}")
	if err != nil {
		return err
	}
	if !imageID.MatchString(id) {
		return errors.New("invalid private image identity")
	}
	if err := writeNew(filepath.Join(c.config.StateDirectory, "image-id-"+key+".json"), imageRecord{tag, id}); err != nil {
		return err
	}
	if _, err := c.call(ctx, "kind", "load", "docker-image", tag, "--name", c.config.Name); err != nil {
		return err
	}
	nodes, err = c.OwnedNodes(ctx)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		// Compatibility aliases exist only inside exact owned nodes, never on the daemon.
		if _, err := c.call(ctx, "docker", "exec", node, "ctr", "--namespace=k8s.io", "images", "tag", "--force", NormalizedImage(tag), NormalizedImage(image.Alias)); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cluster) imageRecords() ([]imageRecord, error) {
	paths, err := filepath.Glob(filepath.Join(c.config.StateDirectory, "image-*.json"))
	if err != nil {
		return nil, err
	}
	records := []imageRecord{}
	var incomplete error
	for _, path := range paths {
		name := filepath.Base(path)
		if strings.HasPrefix(name, "image-id-") {
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(name, "image-"), ".json")
		if !imageKey.MatchString(key) {
			return nil, errors.New("invalid image attempt ledger")
		}
		var attempt imageAttempt
		if readLedger(path, &attempt) != nil {
			return nil, errors.New("invalid image attempt ledger")
		}
		tag, expectedKey := c.imageTag(attempt.Alias)
		if attempt.Alias == "" || attempt.Tag != tag || expectedKey != key {
			return nil, errors.New("image attempt belongs to a different execution")
		}
		var record imageRecord
		if readLedger(filepath.Join(c.config.StateDirectory, "image-id-"+key+".json"), &record) != nil {
			incomplete = errors.New("interrupted image preparation; ledger review required")
			continue
		}
		if record.Tag != tag || !imageID.MatchString(record.ID) {
			return nil, errors.New("invalid private image ownership receipt")
		}
		records = append(records, record)
	}
	// A completion receipt without its attempt is not authority to delete an image.
	complete, err := filepath.Glob(filepath.Join(c.config.StateDirectory, "image-id-*.json"))
	if err != nil || len(complete) != len(records) {
		return records, errors.New("image receipt has no matching preparation attempt")
	}
	return records, incomplete
}
