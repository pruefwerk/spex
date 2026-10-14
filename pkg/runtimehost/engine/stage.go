package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
)

// Stage a GitHub-shared Action's trusted runtime files without cloning a private
// repository with the caller token. Every execution receives its own writable
// copy; downloaded Action directories are never used as execution workspaces.
func stageRuntime(ctx context.Context, args []string, env func(string) string, stdout io.Writer) error {
	flags := flag.NewFlagSet("stage", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "trusted shared Action repository")
	config := flags.String("config", "spex-runtime.toml", "trusted definition relative to source")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *source == "" {
		return errors.New("stage requires a trusted source and definition")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := filepath.Abs(*source)
	if err != nil {
		return err
	}
	definition := filepath.Clean(*config)
	if filepath.IsAbs(definition) || definition == "." || definition == ".." || strings.HasPrefix(definition, ".."+string(filepath.Separator)) || strings.ContainsAny(definition, "\x00\r\n") {
		return errors.New("staged definition must stay inside trusted source")
	}
	if _, err := hostconfig.Load(filepath.Join(root, definition)); err != nil {
		return err
	}
	definitionData, err := os.ReadFile(filepath.Join(root, definition))
	if err != nil {
		return err
	}
	parent := env("RUNNER_TEMP")
	if parent == "" {
		parent = os.TempDir()
	}
	destination, err := os.MkdirTemp(parent, "spex-runtime-")
	if err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(destination)
		}
	}()
	hash := sha256.New()
	count := 0
	var total int64
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if filepath.ToSlash(rel) == "spex-runtime.toml" {
			return nil
		}
		first := strings.Split(filepath.ToSlash(rel), "/")[0]
		switch first {
		case ".git", ".github", ".spex", ".secrets", "artifacts":
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("trusted runtime contains a symlink")
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("runtime entry is not a regular file")
		}
		count++
		total += info.Size()
		if count > 10000 || info.Size() > 64<<20 || total > 256<<20 {
			return errors.New("runtime bundle exceeds limits")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if info.Mode()&0111 != 0 {
			mode = 0700
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		fmt.Fprintf(hash, "%s\x00%o\x00%x\n", filepath.ToSlash(rel), mode, digest)
		return nil
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(destination, "spex-runtime.toml"), definitionData, 0600); err != nil {
		return err
	}
	activeDigest := sha256.Sum256(definitionData)
	fmt.Fprintf(hash, "spex-runtime.toml\x00%o\x00%x\n", 0600, activeDigest)
	release := "bundle-" + hex.EncodeToString(hash.Sum(nil))
	if err := os.Mkdir(filepath.Join(destination, ".spex"), 0700); err != nil {
		return err
	}
	data, _ := json.Marshal(struct {
		Release string `json:"release"`
	}{release})
	if err := writePrivate(filepath.Join(destination, ".spex/runtime-release.json"), data); err != nil {
		return err
	}
	if err := appendCI(env("GITHUB_OUTPUT"), "runtime-directory="+destination+"\nruntime-config="+filepath.Join(destination, "spex-runtime.toml")+"\nruntime-release="+release+"\n"); err != nil {
		return err
	}
	success = true
	fmt.Fprintln(stdout, "Trusted runtime staged: "+release)
	return nil
}
