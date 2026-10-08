package ownedkind

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Fingerprint binds an image plan to explicit host-owned source inputs. It
// hashes content, not timestamps or absolute checkout paths, and rejects links.
// Hosts select inputs; this library never guesses where fixtures or secrets live.
func Fingerprint(root string, images []Image, inputs []string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	relative := func(path string) (string, error) {
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		ref, err := filepath.Rel(root, path)
		if err != nil || ref == ".." || strings.HasPrefix(ref, ".."+string(filepath.Separator)) {
			return "", errors.New("image fingerprint source outside checkout")
		}
		return filepath.ToSlash(ref), nil
	}
	plan := slices.Clone(images)
	for i, image := range plan {
		if image.Build == nil {
			continue
		}
		build := *image.Build
		build.Context, err = relative(build.Context)
		if err != nil {
			return "", err
		}
		build.Dockerfile, err = relative(build.Dockerfile)
		if err != nil {
			return "", err
		}
		build.Contexts = map[string]string{}
		for key, value := range image.Build.Contexts {
			build.Contexts[key], err = relative(value)
			if err != nil {
				return "", err
			}
		}
		plan[i].Build = &build
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write(data)
	paths := map[string]bool{}
	for _, input := range inputs {
		ref, err := relative(input)
		if err != nil {
			return "", err
		}
		if ref == "." {
			return "", errors.New("image fingerprint requires explicit source paths")
		}
		current := root
		for _, part := range strings.Split(filepath.FromSlash(ref), string(filepath.Separator)) {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("image fingerprint input unavailable or linked")
			}
		}
		if err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(ref)), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return errors.New("image fingerprint input unavailable")
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("image fingerprint input must not be a link")
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return errors.New("image fingerprint input must be regular")
			}
			ref, err := relative(path)
			if err != nil {
				return err
			}
			paths[ref] = true
			return nil
		}); err != nil {
			return "", err
		}
	}
	keys := make([]string, 0, len(paths))
	for key := range paths {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, ref := range keys {
		file, err := os.Open(filepath.Join(root, filepath.FromSlash(ref)))
		if err != nil {
			return "", errors.New("image fingerprint source unavailable")
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			file.Close()
			return "", errors.New("image fingerprint source invalid")
		}
		_, _ = fmt.Fprintf(hash, "\n%d:%s:%d:%o:", len(ref), ref, info.Size(), info.Mode().Perm())
		copied, err := io.Copy(hash, file)
		closeErr := file.Close()
		if err != nil || closeErr != nil || copied != info.Size() {
			return "", errors.New("image fingerprint source changed during read")
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
