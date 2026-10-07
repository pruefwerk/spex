package definition

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/pruefwerk/spex/pkg/scenario"
)

// ExpandFiles uses the receiver's pinned source tree. ** matches zero
// or more complete directory components; other components use path.Match.
// Directory symlinks are never traversed. File symlinks must stay inside root.
func ExpandFiles(root string, patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	if len(patterns) > 1000 {
		return nil, errors.New("too many definition patterns")
	}
	parts := make([][]string, len(patterns))
	for i, pattern := range patterns {
		if len(pattern) > 4096 {
			return nil, errors.New("definition pattern exceeds length limit")
		}
		if strings.ContainsAny(pattern, "\\\x00\r\n:") {
			return nil, errors.New("invalid definition pattern")
		}
		clean, err := scenario.RelativePath(pattern)
		if err != nil {
			return nil, errors.New("definition pattern must stay inside source root")
		}
		parts[i] = strings.Split(clean, "/")
		if len(parts[i]) > 256 {
			return nil, errors.New("definition pattern exceeds depth limit")
		}
		for _, segment := range parts[i] {
			if segment != "**" && strings.Contains(segment, "**") {
				return nil, errors.New("** must occupy a complete path component")
			}
			if _, err := path.Match(segment, ""); err != nil {
				return nil, errors.New("invalid definition glob syntax")
			}
		}
	}
	found := map[string]bool{}
	matched := make([]bool, len(patterns))
	visited := 0
	err := fs.WalkDir(os.DirFS(root), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return errors.New("cannot inspect definition source tree")
		}
		visited++
		if visited > 100000 {
			return errors.New("definition source tree exceeds scan limit")
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		for i, pattern := range parts {
			if !matchDefinitionParts(pattern, strings.Split(name, "/")) {
				continue
			}
			resolved, err := scenario.SourcePath(root, name)
			if err != nil {
				return errors.New("definition match escapes source root")
			}
			info, err := os.Stat(resolved)
			if err != nil || !info.Mode().IsRegular() {
				return errors.New("definition match is not a regular file")
			}
			matched[i] = true
			found[name] = true
			if len(found) > 1000 {
				return errors.New("too many matching definition files")
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ok := range matched {
		if !ok {
			return nil, errors.New("definition pattern matched no files")
		}
	}
	files := make([]string, 0, len(found))
	for name := range found {
		files = append(files, name)
	}
	sort.Strings(files)
	return files, nil
}

func matchDefinitionParts(pattern, name []string) bool {
	type position struct{ p, n int }
	memo, seen := map[position]bool{}, map[position]bool{}
	var match func(int, int) bool
	match = func(p, n int) bool {
		key := position{p, n}
		if seen[key] {
			return memo[key]
		}
		seen[key] = true
		ok := false
		if p == len(pattern) {
			ok = n == len(name)
		} else if pattern[p] == "**" {
			ok = match(p+1, n) || (n < len(name) && match(p, n+1))
		} else if n < len(name) {
			segment, _ := path.Match(pattern[p], name[n])
			ok = segment && match(p+1, n+1)
		}
		memo[key] = ok
		return ok
	}
	return match(0, 0)
}
