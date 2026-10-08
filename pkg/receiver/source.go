package receiver

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// GitHubSource fetches immutable caller data. Hosts authenticate source identities
// and own allowlists before using it. It never executes downloaded content.
type GitHubSource struct {
	Token  string
	Client *http.Client
}

func boundedSource(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("source response exceeds limit or is unavailable")
	}
	return data, nil
}

func (g GitHubSource) request(ctx context.Context, address string, authenticated bool) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("source request invalid")
	}
	if authenticated {
		if g.Token == "" {
			return nil, errors.New("source credentials required")
		}
		request.Header.Set("Authorization", "Bearer "+g.Token)
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	private := *client
	private.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := private.Do(request)
	if err != nil {
		return nil, errors.New("source request failed")
	}
	return response, nil
}

func (g GitHubSource) Get(ctx context.Context, ref string, destination any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	response, err := g.request(ctx, "https://api.github.com"+ref, true)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("source metadata request failed")
	}
	data, err := boundedSource(response.Body, 2<<20)
	if err != nil {
		return err
	}
	if err := checkUniqueJSON(data, true); err != nil {
		return errors.New("ambiguous source metadata")
	}
	if json.Unmarshal(data, destination) != nil {
		return errors.New("source metadata invalid")
	}
	return nil
}

func (g GitHubSource) Download(ctx context.Context, ref string, limit int64) ([]byte, error) {
	if limit < 1 || limit > 128<<20 {
		return nil, errors.New("invalid source download limit")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	response, err := g.request(ctx, "https://api.github.com"+ref, true)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == 302 {
		location := response.Header.Get("Location")
		response.Body.Close()
		target, err := url.Parse(location)
		if err != nil || target.Scheme != "https" || target.User != nil || (target.Port() != "" && target.Port() != "443") || !(target.Hostname() == "codeload.github.com" || strings.HasSuffix(target.Hostname(), ".githubusercontent.com") || strings.HasSuffix(target.Hostname(), ".blob.core.windows.net")) {
			return nil, errors.New("source redirect not approved")
		}
		response, err = g.request(ctx, location, false)
		if err != nil {
			return nil, err
		}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errors.New("source download failed")
	}
	return boundedSource(response.Body, limit)
}

// ExtractSource accepts only a bounded, single-root tar.gz containing regular
// files and directories. Files stay read-only and never retain executable bits.
func ExtractSource(ctx context.Context, data []byte, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return errors.New("source destination unavailable")
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return errors.New("source archive invalid")
	}
	defer gz.Close()
	archive := tar.NewReader(gz)
	var total int64
	count := 0
	prefix := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		member, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("source archive invalid")
		}
		count++
		total += member.Size
		if count > 10000 || total > 128<<20 || member.Size < 0 || member.Size > 16<<20 {
			return errors.New("source archive exceeds limits")
		}
		name := strings.TrimSuffix(member.Name, "/")
		parts := strings.Split(name, "/")
		if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || path.Clean(name) != name {
			return errors.New("source archive escapes checkout")
		}
		for _, part := range parts {
			if part == ".." || part == "." || part == "" {
				return errors.New("source archive escapes checkout")
			}
		}
		if prefix == "" {
			prefix = parts[0]
		}
		if parts[0] != prefix || (member.Typeflag != tar.TypeDir && member.Typeflag != tar.TypeReg) {
			return errors.New("source archive entries unsupported")
		}
		target := filepath.Join(append([]string{destination}, parts[1:]...)...)
		if member.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0700); err != nil {
				return errors.New("source directory unavailable")
			}
			continue
		}
		if len(parts) < 2 {
			return errors.New("source archive has no repository root")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return errors.New("source directory unavailable")
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("source entry unavailable")
		}
		_, copyErr := io.CopyN(file, archive, member.Size)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || os.Chmod(target, 0444) != nil {
			return errors.New("source entry incomplete")
		}
	}
	if count == 0 {
		return errors.New("source archive empty")
	}
	return filepath.WalkDir(destination, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return errors.New("source permissions unavailable")
		}
		if entry.IsDir() {
			return os.Chmod(name, 0555)
		}
		return nil
	})
}
