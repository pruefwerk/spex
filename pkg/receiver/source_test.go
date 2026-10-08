package receiver

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type sourceTransport func(*http.Request) (*http.Response, error)

func (f sourceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func sourceTar(t *testing.T, entries []tar.Header) []byte {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	archive := tar.NewWriter(gz)
	for _, entry := range entries {
		if err := archive.WriteHeader(&entry); err != nil {
			t.Fatal(err)
		}
		if entry.Typeflag == tar.TypeReg {
			if _, err := archive.Write([]byte(strings.Repeat("x", int(entry.Size)))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestSourceExtractionIsReadOnlyAndRejectsUnsupportedEntries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "source")
	data := sourceTar(t, []tar.Header{{Name: "repo/acceptance/test.feature", Typeflag: tar.TypeReg, Mode: 0755, Size: 3}})
	if err := ExtractSource(context.Background(), data, dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "acceptance/test.feature"))
	if err != nil || info.Mode().Perm() != 0444 {
		t.Fatal("source became executable or writable")
	}
	info, _ = os.Stat(dir)
	if info.Mode().Perm() != 0555 {
		t.Fatal("source directory writable")
	}
	// Restore fixture directory permissions so Go can remove its temporary files.
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				return os.Chmod(path, 0700)
			}
			return nil
		})
	})
	for _, entries := range [][]tar.Header{
		{{Name: "repo/../escape", Typeflag: tar.TypeReg}}, {{Name: "/repo/file", Typeflag: tar.TypeReg}}, {{Name: "repo/link", Typeflag: tar.TypeSymlink, Linkname: "/outside"}}, {{Name: "repo/a", Typeflag: tar.TypeReg}, {Name: "other/a", Typeflag: tar.TypeReg}}, {{Name: "repo/a", Typeflag: tar.TypeReg}, {Name: "repo/a", Typeflag: tar.TypeReg}}, {{Name: "repo\\file", Typeflag: tar.TypeReg}}, nil,
	} {
		target := filepath.Join(t.TempDir(), "rejected")
		if ExtractSource(context.Background(), sourceTar(t, entries), target) == nil {
			t.Fatal("unsupported source accepted", entries)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	target := filepath.Join(t.TempDir(), "cancelled")
	if ExtractSource(ctx, data, target) == nil {
		t.Fatal("cancelled source extracted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("cancelled extraction created directory")
	}
}

func TestGitHubSourceRedirectsDropCredentialsAndStayBounded(t *testing.T) {
	calls := 0
	api := GitHubSource{Token: "SENTINEL_SOURCE_TOKEN", Client: &http.Client{Transport: sourceTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if r.Header.Get("Authorization") == "" {
				t.Fatal("missing authentication")
			}
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://codeload.github.com/repo/archive"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if r.Header.Get("Authorization") != "" || r.URL.Host != "codeload.github.com" {
			t.Fatal("credentials forwarded")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("source"))}, nil
	})}}
	data, err := api.Download(context.Background(), "/repos/owner/repo/tarball/commit", 8)
	if err != nil || string(data) != "source" || calls != 2 {
		t.Fatal(data, err, calls)
	}
	for _, location := range []string{"https://foreign.invalid/path", "http://codeload.github.com/path", "https://token@codeload.github.com/path", "https://codeload.github.com:444/path"} {
		api.Client.Transport = sourceTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{location}}, Body: io.NopCloser(strings.NewReader("SENTINEL_RAW"))}, nil
		})
		if _, err := api.Download(context.Background(), "/source", 8); err == nil || strings.Contains(err.Error(), "SENTINEL") {
			t.Fatal("redirect accepted or secrets exposed")
		}
	}
	api.Client.Transport = sourceTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("123456789"))}, nil
	})
	if _, err := api.Download(context.Background(), "/source", 8); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestGitHubMetadataPermitsNullButRejectsDuplicateFields(t *testing.T) {
	api := GitHubSource{Token: "token", Client: &http.Client{}}
	for _, body := range []string{`{"conclusion":null}`, `{"id":1,"id":2}`} {
		api.Client.Transport = sourceTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		var result map[string]any
		err := api.Get(context.Background(), "/source", &result)
		if (err == nil) != (body == `{"conclusion":null}`) {
			t.Fatal(body, err)
		}
	}
}
