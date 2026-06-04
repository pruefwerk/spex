package spex

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pruefwerk/spex/internal/workspace"
)

func TestVersionJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"version", "--format", "json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Version     string `json:"version"`
		BuildCommit string `json:"buildCommit"`
		BuildDate   string `json:"buildDate"`
		GoVersion   string `json:"goVersion"`
		GOOS        string `json:"goos"`
		GOARCH      string `json:"goarch"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("version json is invalid: %v\n%s", err, stdout.String())
	}
	if parsed.Version != Version || parsed.GoVersion == "" || parsed.GOOS == "" || parsed.GOARCH == "" {
		t.Fatalf("version json missing fields: %+v", parsed)
	}
}

func TestRootGitHubWorkflowKeepsProductionGates(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"persist-credentials: false",
		"make install-vulncheck",
		"make security-check",
		"make production-candidate-check",
		"SPEX_MQTT_PASSWORD",
		"SPEX_GRAPHQL_TOKEN",
		"SPEX_GRAPHQL_KEYCLOAK_CLIENT_SECRET",
		"SPEX_KEYCLOAK_ADMIN_PASSWORD",
		"Scrub live kubeconfigs",
		"find generated -path '*-live/kubeconfig' -type f -delete",
		"Scan live artifacts for secret values",
		"--scan-artifacts generated/mqtt-ingestion-basic-live",
		"--scan-artifacts generated/mqtt-ingestion-basic-keycloak-live",
	} {
		if !strings.Contains(string(workflow), want) {
			t.Fatalf("root workflow missing %q:\n%s", want, string(workflow))
		}
	}
}

func TestReleaseManifestWritesEscapedMetadata(t *testing.T) {
	dir := t.TempDir()
	writeReleaseArtifactPlaceholders(t, dir)
	oldVersion, oldCommit, oldBuildDate := Version, BuildCommit, BuildDate
	Version = "1.2.3-rc.1"
	BuildCommit = "commit:abc#123"
	BuildDate = "2026-05-31T00:00:00Z"
	t.Cleanup(func() {
		Version, BuildCommit, BuildDate = oldVersion, oldCommit, oldBuildDate
	})

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"release", "manifest", "--dist", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadReleaseManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != Version || manifest.BuildCommit != BuildCommit || manifest.BuildDate != BuildDate {
		t.Fatalf("manifest metadata mismatch: %+v", manifest)
	}
	if len(manifest.Artifacts) != len(releaseArtifacts()) {
		t.Fatalf("manifest artifact count mismatch: %+v", manifest.Artifacts)
	}
}

func TestReleaseManifestNormalizesExistingFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := t.TempDir()
	writeReleaseArtifactPlaceholders(t, dir)
	path := filepath.Join(dir, "release-manifest.yaml")
	if err := os.WriteFile(path, []byte("stale\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldVersion, oldCommit, oldBuildDate := Version, BuildCommit, BuildDate
	Version = "1.2.3"
	BuildCommit = "abc123"
	BuildDate = "2026-05-31T00:00:00Z"
	t.Cleanup(func() {
		Version, BuildCommit, BuildDate = oldVersion, oldCommit, oldBuildDate
	})

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"release", "manifest", "--dist", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	assertFileMode(t, path, 0o644)
	assertNoReleaseMetadataTemps(t, dir)
}

func TestReleaseManifestRejectsUnsafeBuildMetadata(t *testing.T) {
	dir := t.TempDir()
	writeReleaseArtifactPlaceholders(t, dir)

	tests := []struct {
		name        string
		version     string
		buildCommit string
		buildDate   string
		want        string
	}{
		{
			name:        "unsafe version",
			version:     "bad/version",
			buildCommit: "abc123",
			buildDate:   "2026-05-31T00:00:00Z",
			want:        `release manifest: version "bad/version" is not safe for artifact names`,
		},
		{
			name:        "unsafe build commit",
			version:     "1.2.3",
			buildCommit: "abc123 dirty",
			buildDate:   "2026-05-31T00:00:00Z",
			want:        `release manifest: buildCommit "abc123 dirty" is not safe for release metadata`,
		},
		{
			name:        "invalid build date",
			version:     "1.2.3",
			buildCommit: "abc123",
			buildDate:   "31-05-2026",
			want:        "release manifest: buildDate must be RFC3339",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldVersion, oldCommit, oldBuildDate := Version, BuildCommit, BuildDate
			Version = tt.version
			BuildCommit = tt.buildCommit
			BuildDate = tt.buildDate
			t.Cleanup(func() {
				Version, BuildCommit, BuildDate = oldVersion, oldCommit, oldBuildDate
			})

			var stdout, stderr bytes.Buffer
			err := Run([]string{"release", "manifest", "--dist", dir}, &stdout, &stderr)
			if err == nil {
				t.Fatal("expected release manifest to fail")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestReleaseManifestRejectsSymlinkDistArtifact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := t.TempDir()
	writeReleaseArtifactPlaceholders(t, dir)
	target := filepath.Join(dir, "target-probe")
	if err := os.Rename(filepath.Join(dir, "spex-probe"), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "spex-probe")); err != nil {
		t.Fatal(err)
	}
	oldVersion, oldCommit, oldBuildDate := Version, BuildCommit, BuildDate
	Version = "1.2.3"
	BuildCommit = "abc123"
	BuildDate = "2026-05-31T00:00:00Z"
	t.Cleanup(func() {
		Version, BuildCommit, BuildDate = oldVersion, oldCommit, oldBuildDate
	})

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "manifest", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release manifest to fail")
	}
	if !strings.Contains(err.Error(), "artifact spex-probe: not a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseManifestRejectsInvalidSourceArtifactMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := t.TempDir()
	writeReleaseArtifactPlaceholders(t, dir)
	if err := os.Chmod(filepath.Join(dir, "release-provenance.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldVersion, oldCommit, oldBuildDate := Version, BuildCommit, BuildDate
	Version = "1.2.3"
	BuildCommit = "abc123"
	BuildDate = "2026-05-31T00:00:00Z"
	t.Cleanup(func() {
		Version, BuildCommit, BuildDate = oldVersion, oldCommit, oldBuildDate
	})

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "manifest", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release manifest to fail")
	}
	if !strings.Contains(err.Error(), "artifact release-provenance.json: must not be executable") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "release-manifest.yaml")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no release manifest, got stat err %v", statErr)
	}
}

func TestReleaseManifestRejectsOverPermissiveSourceArtifactMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := t.TempDir()
	writeReleaseArtifactPlaceholders(t, dir)
	if err := os.Chmod(filepath.Join(dir, "spex"), 0o775); err != nil {
		t.Fatal(err)
	}
	oldVersion, oldCommit, oldBuildDate := Version, BuildCommit, BuildDate
	Version = "1.2.3"
	BuildCommit = "abc123"
	BuildDate = "2026-05-31T00:00:00Z"
	t.Cleanup(func() {
		Version, BuildCommit, BuildDate = oldVersion, oldCommit, oldBuildDate
	})

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "manifest", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release manifest to fail")
	}
	if !strings.Contains(err.Error(), "artifact spex: mode mismatch: got 0775 want 0755") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseProvenanceRejectsUnsafeBuildMetadata(t *testing.T) {
	dir := t.TempDir()
	oldVersion, oldCommit, oldBuildDate := Version, BuildCommit, BuildDate
	Version = "1.2.3"
	BuildCommit = "abc123\n"
	BuildDate = "2026-05-31T00:00:00Z"
	t.Cleanup(func() {
		Version, BuildCommit, BuildDate = oldVersion, oldCommit, oldBuildDate
	})

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "provenance", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release provenance to fail")
	}
	if !strings.Contains(err.Error(), `release provenance: buildCommit "abc123\n" is not safe for release metadata`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "release-provenance.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("release provenance should not be written, statErr=%v", statErr)
	}
}

func TestReleaseManifestRejectsUnsafePlatformFields(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "release-manifest.yaml"), "goos: "+runtime.GOOS, "goos: bad/os")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `release manifest: goos "bad/os" is not safe for artifact names`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadBinaryVersionTruncatesFailingBinaryOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	path := filepath.Join(t.TempDir(), "spex")
	content := `#!/bin/sh
yes x | head -c ` + fmt.Sprintf("%d", int(maxReleaseCommandOutputSize)+8) + `
exit 2
`
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := readBinaryVersion(path, "spex")
	if err == nil {
		t.Fatal("expected readBinaryVersion to fail")
	}
	if !strings.Contains(err.Error(), "command output truncated") {
		t.Fatalf("expected truncation marker, got %v", err)
	}
}

func TestReleaseProvenanceNormalizesExistingFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "release-provenance.json")
	if err := os.WriteFile(path, []byte("stale\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldVersion, oldCommit, oldBuildDate := Version, BuildCommit, BuildDate
	Version = "1.2.3"
	BuildCommit = "abc123"
	BuildDate = "2026-05-31T00:00:00Z"
	t.Cleanup(func() {
		Version, BuildCommit, BuildDate = oldVersion, oldCommit, oldBuildDate
	})

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"release", "provenance", "--dist", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	assertFileMode(t, path, 0o644)
	assertNoReleaseMetadataTemps(t, dir)
}

func TestReleaseChecksumWritesReleaseChecksums(t *testing.T) {
	dir := t.TempDir()
	writeReleaseArtifactPlaceholders(t, dir)
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"release", "checksum", "--dist", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	checksums, err := loadReleaseChecksums(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range releaseArtifacts() {
		expected, err := fileSHA256(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if checksums[name] != expected {
			t.Fatalf("checksum mismatch for %s", name)
		}
	}
}

func TestReleaseChecksumRejectsSymlinkDistArtifact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := t.TempDir()
	writeReleaseArtifactPlaceholders(t, dir)
	target := filepath.Join(dir, "target-probe")
	if err := os.Rename(filepath.Join(dir, "spex-probe"), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "spex-probe")); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "checksum", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release checksum to fail")
	}
	if !strings.Contains(err.Error(), "artifact spex-probe: not a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseChecksumRejectsInvalidSourceArtifactMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := t.TempDir()
	writeReleaseArtifactPlaceholders(t, dir)
	if err := os.Chmod(filepath.Join(dir, "spex-probe"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "checksum", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release checksum to fail")
	}
	if !strings.Contains(err.Error(), "artifact spex-probe: must be executable") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "SHA256SUMS")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no checksums, got stat err %v", statErr)
	}
}

func TestReleaseChecksumNormalizesExistingFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := t.TempDir()
	writeReleaseArtifactPlaceholders(t, dir)
	path := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(path, []byte("stale\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"release", "checksum", "--dist", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	assertFileMode(t, path, 0o644)
	assertNoReleaseMetadataTemps(t, dir)
}

func TestReleaseChecksumWritesArchiveSidecar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	if err := writeReleaseArchive(dir, archivePath, false); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"release", "checksum", "--archive", archivePath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	checksums, err := loadReleaseChecksums(archivePath + ".sha256")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := fileSHA256(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if checksums[filepath.Base(archivePath)] != expected {
		t.Fatalf("archive checksum mismatch")
	}
}

func TestReleaseChecksumArchiveRejectsExplicitDist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	if err := writeReleaseArchive(dir, archivePath, false); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "checksum", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release checksum to fail")
	}
	if !strings.Contains(err.Error(), "release checksum --archive cannot be combined with --dist") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(archivePath + ".sha256"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no archive checksum sidecar, got stat err %v", statErr)
	}
}

func TestReleaseChecksumNormalizesExistingArchiveSidecarMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	if err := writeReleaseArchive(dir, archivePath, false); err != nil {
		t.Fatal(err)
	}
	sidecarPath := archivePath + ".sha256"
	if err := os.WriteFile(sidecarPath, []byte("stale\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"release", "checksum", "--archive", archivePath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	assertFileMode(t, sidecarPath, 0o644)
	assertNoReleaseMetadataTemps(t, dir)
}

func TestReleaseChecksumRejectsNonCanonicalArchiveName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, "spex_wrong.tar.gz")
	if err := os.WriteFile(archivePath, []byte("archive\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "checksum", "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release checksum to fail")
	}
	if !strings.Contains(err.Error(), "expected archive name "+releaseArchiveNameForVersion("1.2.3")) {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(archivePath + ".sha256"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no checksum sidecar, got stat err %v", statErr)
	}
}

func TestReleaseChecksumRejectsSymlinkArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	targetPath := filepath.Join(dir, "target.tar.gz")
	if err := os.WriteFile(targetPath, []byte("archive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	if err := os.Symlink(targetPath, archivePath); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "checksum", "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release checksum to fail")
	}
	if !strings.Contains(err.Error(), "archive "+filepath.Base(archivePath)+": not a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(archivePath + ".sha256"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no checksum sidecar, got stat err %v", statErr)
	}
}

func TestReleaseChecksumRejectsOversizedArchive(t *testing.T) {
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	file, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxReleaseArchiveSize + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(archivePath, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err = Run([]string{"release", "checksum", "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release checksum to fail")
	}
	if !strings.Contains(err.Error(), "file is too large") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(archivePath + ".sha256"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no checksum sidecar, got stat err %v", statErr)
	}
}

func TestReleaseArchiveWritesVerifiableArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archiveName := releaseArchiveNameForVersion("1.2.3")

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"release", "archive", "--dist", dir, "--name", archiveName}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, archiveName)
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatal(err)
	}
	assertFileMode(t, archivePath, 0o644)
	assertNoReleaseMetadataTemps(t, dir)
	if err := Run([]string{"release", "checksum", "--archive", archivePath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseArchiveRefusesOverwrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archiveName := releaseArchiveNameForVersion("1.2.3")
	if err := os.WriteFile(filepath.Join(dir, archiveName), []byte("existing\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "archive", "--dist", dir, "--name", archiveName}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release archive to fail")
	}
	if !strings.Contains(err.Error(), "file exists") && !strings.Contains(err.Error(), "exists") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseArchiveRejectsNonCanonicalName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "archive", "--dist", dir, "--name", "spex_wrong.tar.gz"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release archive to fail")
	}
	if !strings.Contains(err.Error(), "release archive --name must be "+releaseArchiveNameForVersion("1.2.3")) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseArchiveFailsForInvalidSourceArtifactMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archiveName := releaseArchiveNameForVersion("1.2.3")
	if err := os.Chmod(filepath.Join(dir, "version.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "archive", "--dist", dir, "--name", archiveName}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release archive to fail")
	}
	if !strings.Contains(err.Error(), "release archive: artifact version.json: must not be executable") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, archiveName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no archive to be written, got stat err %v", statErr)
	}
	assertNoReleaseMetadataTemps(t, dir)
}

func TestReleaseArchiveFailsForOverPermissiveSourceArtifactMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archiveName := releaseArchiveNameForVersion("1.2.3")
	if err := os.Chmod(filepath.Join(dir, "version.json"), 0o664); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "archive", "--dist", dir, "--name", archiveName}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release archive to fail")
	}
	if !strings.Contains(err.Error(), "release archive: artifact version.json: mode mismatch: got 0664 want 0644") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, archiveName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no archive to be written, got stat err %v", statErr)
	}
	assertNoReleaseMetadataTemps(t, dir)
}

func TestReleaseArchiveForceOverwritesExistingArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archiveName := releaseArchiveNameForVersion("1.2.3")
	if err := os.WriteFile(filepath.Join(dir, archiveName), []byte("existing\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"release", "archive", "--dist", dir, "--name", archiveName, "--force"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, archiveName)
	assertFileMode(t, archivePath, 0o644)
	assertNoReleaseMetadataTemps(t, dir)
	if err := Run([]string{"release", "checksum", "--archive", archivePath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseVerifyFailsForExecutableArchiveFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchive(t, dir, archivePath)
	if err := os.Chmod(archivePath, 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "archive "+filepath.Base(archivePath)+": must not be executable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForOverPermissiveArchiveFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchive(t, dir, archivePath)
	if err := os.Chmod(archivePath, 0o664); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "archive "+filepath.Base(archivePath)+": mode mismatch: got 0664 want 0644") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForOversizedArchiveFile(t *testing.T) {
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	file, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxReleaseArchiveSize + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(archivePath, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFakeReleaseArchiveChecksumForPath(t, archivePath)

	var stdout, stderr bytes.Buffer
	err = Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "file is too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForSymlinkArchiveFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archiveDir := t.TempDir()
	targetPath := filepath.Join(archiveDir, "target.tar.gz")
	writeFakeReleaseArchive(t, dir, targetPath)
	archivePath := filepath.Join(archiveDir, releaseArchiveNameForVersion("1.2.3"))
	if err := os.Symlink(targetPath, archivePath); err != nil {
		t.Fatal(err)
	}
	writeFakeReleaseArchiveChecksumForPathWithName(t, targetPath, filepath.Base(archivePath), archivePath+".sha256")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "archive "+filepath.Base(archivePath)+": not a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForSymlinkReleaseManifest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	manifestPath := filepath.Join(dir, "release-manifest.yaml")
	realManifest := filepath.Join(t.TempDir(), "release-manifest.yaml")
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realManifest, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realManifest, manifestPath); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err = Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "release manifest: not a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForSymlinkReleaseChecksums(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	checksumPath := filepath.Join(dir, "SHA256SUMS")
	realChecksums := filepath.Join(t.TempDir(), "SHA256SUMS")
	content, err := os.ReadFile(checksumPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realChecksums, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(checksumPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realChecksums, checksumPath); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err = Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "SHA256SUMS: not a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForOversizedReleaseManifest(t *testing.T) {
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	path := filepath.Join(dir, "release-manifest.yaml")
	content := bytes.Repeat([]byte("x"), int(maxReleaseTextFileSize("release-manifest.yaml", "release manifest"))+1)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "release manifest: file is too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForOversizedReleaseChecksums(t *testing.T) {
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	path := filepath.Join(dir, "SHA256SUMS")
	content := bytes.Repeat([]byte("x"), int(maxReleaseTextFileSize("SHA256SUMS", "SHA256SUMS"))+1)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "SHA256SUMS: file is too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseArchiveIsReproducible(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	firstDir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	secondDir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archiveName := releaseArchiveNameForVersion("1.2.3")

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"release", "archive", "--dist", firstDir, "--name", archiveName}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"release", "archive", "--dist", secondDir, "--name", archiveName}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(firstDir, archiveName))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(secondDir, archiveName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("release archives are not byte-for-byte reproducible")
	}
}

func TestReleaseVerifyPassesForConsistentReleaseDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "release verified: "+dir) {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

func TestReleaseVerifyFailsForInvalidBuildDate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "release-manifest.yaml"), "buildDate: 2026-05-31T00:00:00Z", "buildDate: 31-05-2026")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "release manifest: buildDate must be RFC3339") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForUnsafeVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "release-manifest.yaml"), "version: 1.2.3", "version: bad/version")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `release manifest: version "bad/version" is not safe for artifact names`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForChecksumMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	if err := os.WriteFile(filepath.Join(dir, "spex-probe"), []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch for spex-probe") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForNonExecutableDistBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	if err := os.Chmod(filepath.Join(dir, "spex-probe"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "artifact spex-probe: must be executable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForExecutableDistMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	if err := os.Chmod(filepath.Join(dir, "version.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "artifact version.json: must not be executable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForInvalidReleaseInventory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	if err := os.WriteFile(filepath.Join(dir, "go-modules.txt"), []byte("example.com/other v1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "go-modules.txt: first module must be") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForUnknownDependencyInventoryField(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "dependency-inventory.json"), `"modules": [`, `"extra": true,`+"\n  "+`"modules": [`)
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `dependency-inventory.json: json: unknown field "extra"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForTrailingDependencyInventoryJSONValue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	appendFile(t, filepath.Join(dir, "dependency-inventory.json"), "{}\n")
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "dependency-inventory.json: unexpected trailing JSON value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForDependencyInventoryModuleMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "dependency-inventory.json"), `"version": "v1.5.1"`, `"version": "v9.9.9"`)
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "dependency-inventory.json: module 1 mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForManifestArtifactOrderMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	writeReleaseManifestWithArtifactOrder(t, dir, []string{"spex-probe", "spex", "spex-probe-influxdb", "spex-probe-redis", "spex-demo-stack", "LICENSE", "COMMERCIAL.md", "CONTRIBUTING.md", "THIRD-PARTY-NOTICES.md", "go-modules.txt", "dependency-inventory.json", "buildinfo.txt", "third-party-licenses.txt", "release-provenance.json"})

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "release manifest: artifact order mismatch at index 0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForUnknownManifestField(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	appendFile(t, filepath.Join(dir, "release-manifest.yaml"), "unexpectedField: true\n")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `field unexpectedField not found`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForTrailingManifestYAMLDocument(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	appendFile(t, filepath.Join(dir, "release-manifest.yaml"), "---\napiVersion: ignored\n")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "release manifest: unexpected trailing YAML document") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForUnknownManifestArtifactField(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "release-manifest.yaml"), "  - path: spex\n", "  - path: spex\n    extra: true\n")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `field extra not found`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForChecksumOrderMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	writeReleaseChecksumsWithOrder(t, dir, []string{"spex-probe", "spex", "spex-probe-influxdb", "spex-probe-redis", "spex-demo-stack", "LICENSE", "COMMERCIAL.md", "CONTRIBUTING.md", "THIRD-PARTY-NOTICES.md", "go-modules.txt", "dependency-inventory.json", "buildinfo.txt", "third-party-licenses.txt", "release-provenance.json"})

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "SHA256SUMS: artifact order mismatch at index 0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForBuildInfoPathLeak(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "buildinfo.txt"), "spex\n", "dist/spex\n")
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "buildinfo.txt: first line must be spex") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForLocalPathLeakInTextArtifact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	appendFile(t, filepath.Join(dir, "third-party-licenses.txt"), "source /Users/example/worktree/LICENSE\n")
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `third-party-licenses.txt: contains local path leak "/Users/"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFirstLocalPathLeakDoesNotFlagURLs(t *testing.T) {
	content := strings.Join([]string{
		"https://github.com/pruefwerk/spex",
		"http://example.com/spex",
		"oci://registry.example.com/charts/spex",
	}, "\n")
	if leaked := firstLocalPathLeak(content); leaked != "" {
		t.Fatalf("unexpected local path leak: %q", leaked)
	}
}

func TestFirstLocalPathLeakFlagsWindowsDrivePath(t *testing.T) {
	if leaked := firstLocalPathLeak("artifact C:/work/example/spex"); leaked != "C:/" {
		t.Fatalf("unexpected leak marker: %q", leaked)
	}
	if leaked := firstLocalPathLeak("artifact C:\\work\\example\\spex"); leaked != "C:\\" {
		t.Fatalf("unexpected leak marker: %q", leaked)
	}
}

func TestReleaseVerifyFailsForIncompleteThirdPartyLicenseInventory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	if err := os.WriteFile(filepath.Join(dir, "third-party-licenses.txt"), []byte("# third-party licenses\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "third-party-licenses.txt: missing module github.com/eclipse/paho.mqtt.golang v1.5.1") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForProvenanceMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "release-provenance.json"), `"buildCommit": "abc123"`, `"buildCommit": "different"`)
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `release-provenance.json: buildCommit "different" does not match manifest "abc123"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForUnknownVersionJSONField(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "version.json"), `"goarch": `+fmt.Sprintf("%q", runtime.GOARCH), `"goarch": `+fmt.Sprintf("%q", runtime.GOARCH)+`,`+"\n  "+`"extra": true`)
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `version.json: json: unknown field "extra"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForMissingVersionJSONField(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "version.json"), `"goVersion": `+fmt.Sprintf("%q", runtime.Version()), `"goVersion": ""`)
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "version.json: version, buildCommit, buildDate, goVersion, goos, and goarch are required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForInvalidVersionJSONGoVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "version.json"), `"goVersion": `+fmt.Sprintf("%q", runtime.Version()), `"goVersion": "toolchain"`)
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `version.json: goVersion "toolchain" is not valid`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForUnknownBinaryVersionJSONField(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	binaryPath := filepath.Join(dir, "spex")
	replaceFileText(t, binaryPath, `"goarch": `+fmt.Sprintf("%q", runtime.GOARCH), `"goarch": `+fmt.Sprintf("%q", runtime.GOARCH)+`,`+"\n  "+`"extra": true`)
	if err := os.Chmod(binaryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `spex version: json: unknown field "extra"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForMissingBinaryVersionJSONField(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	binaryPath := filepath.Join(dir, "spex")
	replaceFileText(t, binaryPath, `"goVersion": `+fmt.Sprintf("%q", runtime.Version()), `"goVersion": ""`)
	if err := os.Chmod(binaryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "spex version: version, buildCommit, buildDate, goVersion, goos, and goarch are required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForInvalidBinaryVersionJSONMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	binaryPath := filepath.Join(dir, "spex")
	replaceFileText(t, binaryPath, `"buildCommit": "abc123"`, `"buildCommit": "bad commit"`)
	if err := os.Chmod(binaryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `spex version: buildCommit "bad commit" is not safe for release metadata`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForTrailingVersionJSONValue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	appendFile(t, filepath.Join(dir, "version.json"), "{}\n")
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "version.json: unexpected trailing JSON value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForTrailingBinaryVersionJSONValue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	binaryPath := filepath.Join(dir, "spex")
	replaceFileText(t, binaryPath, "JSON\nexit 0", "{}\nJSON\nexit 0")
	if err := os.Chmod(binaryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "spex version: unexpected trailing JSON value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForProbeBinaryVersionMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	binaryPath := filepath.Join(dir, "spex-probe")
	replaceFileText(t, binaryPath, `"version": "1.2.3"`, `"version": "9.9.9"`)
	if err := os.Chmod(binaryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `spex-probe version: version "9.9.9" does not match manifest "1.2.3"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForProbeBinaryInvalidVersionJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	binaryPath := filepath.Join(dir, "spex-probe")
	replaceFileText(t, binaryPath, "JSON\nexit 0", "{}\nJSON\nexit 0")
	if err := os.Chmod(binaryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "spex-probe version: unexpected trailing JSON value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForUnknownProvenanceField(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "release-provenance.json"), `"modulePath": `+fmt.Sprintf("%q", modulePath), `"modulePath": `+fmt.Sprintf("%q", modulePath)+`,`+"\n  "+`"extra": true`)
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `release-provenance.json: json: unknown field "extra"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForTrailingProvenanceJSONValue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	appendFile(t, filepath.Join(dir, "release-provenance.json"), "{}\n")
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "release-provenance.json: unexpected trailing JSON value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForProvenanceGoVersionMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "release-provenance.json"), `"goVersion": `+fmt.Sprintf("%q", runtime.Version()), `"goVersion": "go0.0.0"`)
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `release-provenance.json: goVersion "go0.0.0" does not match version.json `) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForMissingProvenanceModulePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "release-provenance.json"), `"modulePath": `+fmt.Sprintf("%q", modulePath), `"modulePath": ""`)
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "release-provenance.json: apiVersion, kind, version, buildCommit, buildDate, goos, goarch, goVersion, and modulePath are required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForInvalidProvenanceBuildDate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "release-provenance.json"), `"buildDate": "2026-05-31T00:00:00Z"`, `"buildDate": "not-a-date"`)
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "release-provenance.json: buildDate must be RFC3339") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForInvalidProvenanceGoVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "release-provenance.json"), `"goVersion": `+fmt.Sprintf("%q", runtime.Version()), `"goVersion": "1.25.0"`)
	updateReleaseChecksumsAndManifest(t, dir)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `release-provenance.json: goVersion "1.25.0" is not valid`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyPassesForArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchive(t, dir, archivePath)

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "release verified: "+dir) {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

func TestReleaseVerifyFailsForArchiveArtifactMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	probeInfo, err := os.Stat(filepath.Join(dir, "spex-probe"))
	if err != nil {
		t.Fatal(err)
	}
	writeFakeReleaseArchiveWithOverrides(t, dir, archivePath, map[string]string{
		"spex-probe": strings.Repeat("x", int(probeInfo.Size())),
	})

	var stdout, stderr bytes.Buffer
	err = Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch for spex-probe") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForArchiveSizeMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchiveWithOverrides(t, dir, archivePath, map[string]string{
		"spex-probe": "tampered probe\n",
	})

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "size mismatch for spex-probe") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForArchiveModeMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchiveWithModes(t, dir, archivePath, map[string]int64{
		"spex": 0o644,
	})

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "mode mismatch for spex: got 0644 want 0755") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForArchiveOrderMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchiveWithOrder(t, dir, archivePath, []string{"spex-probe", "spex", "spex-probe-influxdb", "spex-probe-redis", "spex-demo-stack", "LICENSE", "COMMERCIAL.md", "CONTRIBUTING.md", "THIRD-PARTY-NOTICES.md", "go-modules.txt", "dependency-inventory.json", "buildinfo.txt", "third-party-licenses.txt", "release-provenance.json", "SHA256SUMS", "version.json", "release-manifest.yaml"})

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "file order mismatch at index 0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForArchiveTimestampMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchiveWithModTimes(t, dir, archivePath, map[string]time.Time{
		"spex": time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC),
	}, releaseArchiveTimestamp())

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "timestamp mismatch for spex") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForArchiveGzipTimestampMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchiveWithModTimes(t, dir, archivePath, nil, time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC))

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "gzip timestamp") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForUnexpectedManifestArtifact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	sum := strings.Repeat("0", 64)
	appendFile(t, filepath.Join(dir, "release-manifest.yaml"), "  - path: extra\n    sha256: "+sum+"\n")
	appendFile(t, filepath.Join(dir, "SHA256SUMS"), sum+"  extra\n")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "unexpected artifact extra") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForUnexpectedDistFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	if err := os.WriteFile(filepath.Join(dir, "stale.txt"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "release dist: unexpected file stale.txt") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyAllowsSelectedArchiveFilesInDist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchive(t, dir, archivePath)

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseVerifyAllowsSelectedArchiveFilesInDistWithDifferentRelativeSpelling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "dist")
	writeFakeReleaseDirAt(t, dir, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchive(t, dir, archivePath)
	t.Chdir(parent)

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"release", "verify", "--dist", "./dist", "--archive", filepath.Join("dist", releaseArchiveNameForVersion("1.2.3"))}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseVerifyFailsForUnselectedArchiveFilesInDist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchive(t, dir, archivePath)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "release dist: unexpected file "+releaseArchiveNameForVersion("1.2.3")) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForArchiveNameMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, "spex_wrong.tar.gz")
	writeFakeReleaseArchive(t, dir, archivePath)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "expected archive name "+releaseArchiveNameForVersion("1.2.3")) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForUnexpectedArchiveFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchiveWithOverrides(t, dir, archivePath, map[string]string{
		"extra": "extra\n",
	})

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "unexpected file extra") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForDuplicateChecksumLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	sum, err := fileSHA256(filepath.Join(dir, "spex-probe"))
	if err != nil {
		t.Fatal(err)
	}
	appendFile(t, filepath.Join(dir, "SHA256SUMS"), sum+"  spex-probe\n")

	var stdout, stderr bytes.Buffer
	err = Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "duplicate artifact spex-probe") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForDuplicateArchiveFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchiveWithDuplicates(t, dir, archivePath, []string{"spex-probe"})

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "duplicate file spex-probe") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForInvalidManifestSHA(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "release-manifest.yaml"), "sha256: ", "sha256: not-a-sha-")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "invalid sha256 for spex") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForInvalidChecksumSHA(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceChecksumHash(t, filepath.Join(dir, "SHA256SUMS"), "spex-probe", "not-a-sha")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "invalid sha256 for spex-probe") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForUnsafeChecksumArtifactPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	replaceFileText(t, filepath.Join(dir, "SHA256SUMS"), "  spex-probe\n", "  ../spex-probe\n")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `SHA256SUMS: artifact path "../spex-probe" must be a file name`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForExtraArchiveChecksumLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchive(t, dir, archivePath)
	appendFile(t, archivePath+".sha256", strings.Repeat("0", 64)+"  other.tar.gz\n")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "archive checksum: artifact order mismatch at index 1: got other.tar.gz want <none>") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForUnsafeArchiveChecksumArtifactPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell script as fake release binary")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchive(t, dir, archivePath)
	replaceFileText(t, archivePath+".sha256", "  "+filepath.Base(archivePath)+"\n", "  ../"+filepath.Base(archivePath)+"\n")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), `archive checksum: artifact path "../`+filepath.Base(archivePath)+`" must be a file name`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForExecutableArchiveChecksumSidecar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchive(t, dir, archivePath)
	if err := os.Chmod(archivePath+".sha256", 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "archive checksum: "+filepath.Base(archivePath)+".sha256 must not be executable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForOverPermissiveArchiveChecksumSidecar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archivePath := filepath.Join(dir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchive(t, dir, archivePath)
	if err := os.Chmod(archivePath+".sha256", 0o664); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "archive checksum: "+filepath.Base(archivePath)+".sha256 mode mismatch: got 0664 want 0644") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReleaseVerifyFailsForSymlinkArchiveChecksumSidecar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := writeFakeReleaseDir(t, "1.2.3", "abc123", "2026-05-31T00:00:00Z")
	archiveDir := t.TempDir()
	archivePath := filepath.Join(archiveDir, releaseArchiveNameForVersion("1.2.3"))
	writeFakeReleaseArchive(t, dir, archivePath)
	realSidecar := filepath.Join(archiveDir, "real.sha256")
	sum, err := fileSHA256(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realSidecar, []byte(sum+"  "+filepath.Base(archivePath)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(archivePath + ".sha256"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realSidecar, archivePath+".sha256"); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err = Run([]string{"release", "verify", "--dist", dir, "--archive", archivePath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected release verify to fail")
	}
	if !strings.Contains(err.Error(), "archive checksum: "+filepath.Base(archivePath)+".sha256 is not a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunWorkspaceWritesPassingReport(t *testing.T) {
	workspace := writeWorkspace(t)
	fake := writeFakeKubectl(t, 0, "")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"run", "--workspace", workspace, "--command", fake}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	report := readReport(t, workspace)
	for _, want := range []string{
		"kind: ScenarioRunReport",
		"name: mqtt-ingestion-basic",
		"runId: run-fixed-test",
		"result: passed",
		"scenarioResult: passed",
		"runnerResult: passed",
		"scenarioFile: examples/scenarios/mqtt-ingestion-basic.yaml",
		"bindingFile: examples/bindings/local-dev.yaml",
		"operationId: publish-reading-1",
		"logRef: evidence/logs/03-publish-reading-1.log",
		"jobStatusRef: null",
		"resultRef: null",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestWriteReportNormalizesExistingReportFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	workspace := writeWorkspace(t)
	reportPath := filepath.Join(workspace, "reports", "scenario-run-report.yaml")
	if err := os.WriteFile(reportPath, []byte("stale\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := WriteReport(ReportInput{
		Workspace:      workspace,
		StartedAt:      testTime(),
		FinishedAt:     testTime(),
		ScenarioResult: "passed",
		RunnerResult:   "passed",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileMode(t, reportPath, 0o644)
}

func TestWriteReportRejectsSymlinkReportFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	workspace := writeWorkspace(t)
	reportPath := filepath.Join(workspace, "reports", "scenario-run-report.yaml")
	realReport := filepath.Join(t.TempDir(), "scenario-run-report.yaml")
	if err := os.WriteFile(realReport, []byte("real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realReport, reportPath); err != nil {
		t.Fatal(err)
	}

	_, err := WriteReport(ReportInput{
		Workspace:      workspace,
		StartedAt:      testTime(),
		FinishedAt:     testTime(),
		ScenarioResult: "passed",
		RunnerResult:   "passed",
	})
	if err == nil {
		t.Fatal("expected WriteReport to fail")
	}
	if !strings.Contains(err.Error(), "scenario-run-report.yaml: not a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}
	content, readErr := os.ReadFile(realReport)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "real\n" {
		t.Fatalf("symlink target was modified: %q", string(content))
	}
}

func TestWriteReportRejectsSymlinkReportDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	workspace := writeWorkspace(t)
	reportDir := filepath.Join(workspace, "reports")
	targetDir := t.TempDir()
	if err := os.RemoveAll(reportDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetDir, reportDir); err != nil {
		t.Fatal(err)
	}

	_, err := WriteReport(ReportInput{
		Workspace:      workspace,
		StartedAt:      testTime(),
		FinishedAt:     testTime(),
		ScenarioResult: "passed",
		RunnerResult:   "passed",
	})
	if err == nil {
		t.Fatal("expected WriteReport to fail")
	}
	if !strings.Contains(err.Error(), "refusing symlink directory") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(targetDir, "scenario-run-report.yaml")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected symlink target dir to remain untouched, got stat err %v", statErr)
	}
}

func TestLoadStepMapRejectsUnknownFields(t *testing.T) {
	workspace := writeWorkspace(t)
	appendFile(t, filepath.Join(workspace, "step-map.yaml"), "unexpected: true\n")

	_, err := loadStepMap(workspace)
	if err == nil {
		t.Fatal("expected loadStepMap to fail")
	}
	if !strings.Contains(err.Error(), `step-map.yaml: yaml: unmarshal errors`) || !strings.Contains(err.Error(), `field unexpected not found`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadStepMapRejectsTrailingYAMLDocument(t *testing.T) {
	workspace := writeWorkspace(t)
	appendFile(t, filepath.Join(workspace, "step-map.yaml"), "---\n{}\n")

	_, err := loadStepMap(workspace)
	if err == nil {
		t.Fatal("expected loadStepMap to fail")
	}
	if !strings.Contains(err.Error(), "step-map.yaml: unexpected trailing YAML document") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadStepMapRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	workspace := writeWorkspace(t)
	stepMapPath := filepath.Join(workspace, "step-map.yaml")
	realStepMap := filepath.Join(t.TempDir(), "step-map.yaml")
	content, err := os.ReadFile(stepMapPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realStepMap, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stepMapPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realStepMap, stepMapPath); err != nil {
		t.Fatal(err)
	}

	_, err = loadStepMap(workspace)
	if err == nil {
		t.Fatal("expected loadStepMap to fail")
	}
	if !strings.Contains(err.Error(), "step-map.yaml: not a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadStepMapRejectsOversizedFile(t *testing.T) {
	workspace := writeWorkspace(t)
	content := bytes.Repeat([]byte("x"), int(maxStepMapFileSize)+1)
	if err := os.WriteFile(filepath.Join(workspace, "step-map.yaml"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := loadStepMap(workspace)
	if err == nil {
		t.Fatal("expected loadStepMap to fail")
	}
	if !strings.Contains(err.Error(), "step-map.yaml: file is too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReportReferencesOnlyExistingEvidenceFiles(t *testing.T) {
	workspace := writeWorkspace(t)
	logDir := filepath.Join(workspace, "evidence", "logs")
	resultDir := filepath.Join(workspace, "evidence", "results")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "03-publish-reading-1.log"), []byte("probe log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	probeResult := `{"apiVersion":"spex.probe.result.v0.1","operation":"mqtt.publish","status":"passed"}`
	if err := os.WriteFile(filepath.Join(resultDir, "03-publish-reading-1.jsonl"), []byte(probeResult+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := WriteReport(ReportInput{
		Workspace:      workspace,
		StartedAt:      testTime(),
		FinishedAt:     testTime(),
		ScenarioResult: "passed",
		RunnerResult:   "passed",
	})
	if err != nil {
		t.Fatal(err)
	}
	report := readReport(t, workspace)
	for _, want := range []string{
		"logRef: evidence/logs/03-publish-reading-1.log",
		"resultRef: evidence/results/03-publish-reading-1.jsonl",
		"jobStatusRef: null",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "evidence/status/03-publish-reading-1.job.json") {
		t.Fatalf("report referenced missing job status file:\n%s", report)
	}
}

func TestCompileRejectsInvalidRunIDLabel(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(wd, "..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	var stdout, stderr bytes.Buffer
	err = Run([]string{
		"compile",
		"--scenario", filepath.Join("examples", "scenarios", "mqtt-ingestion-basic.yaml"),
		"--binding", filepath.Join("examples", "bindings", "local-dev.yaml"),
		"--out", filepath.Join(dir, "out"),
		"--run-id", "bad/run",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "run-id must be a Kubernetes label value") {
		t.Fatalf("expected invalid run-id error, got %v", err)
	}
}

func TestValidateOutputDirRejectsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(path, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := validateOutputDir(path)
	if err == nil {
		t.Fatal("expected validateOutputDir to fail")
	}
	if !strings.Contains(err.Error(), "existing path is not a directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateOutputDirRejectsExistingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(target, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := os.Symlink(target, out); err != nil {
		t.Fatal(err)
	}

	err := validateOutputDir(out)
	if err == nil {
		t.Fatal("expected validateOutputDir to fail")
	}
	if !strings.Contains(err.Error(), "existing path is a symlink") {
		t.Fatalf("unexpected error: %v", err)
	}
	content, readErr := os.ReadFile(sentinel)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "keep\n" {
		t.Fatalf("symlink target was modified: %q", string(content))
	}
}

func TestCompileRejectsInvalidNamespaceOverride(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(wd, "..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	var stdout, stderr bytes.Buffer
	err = Run([]string{
		"compile",
		"--scenario", filepath.Join("examples", "scenarios", "mqtt-ingestion-basic.yaml"),
		"--binding", filepath.Join("examples", "bindings", "local-dev.yaml"),
		"--out", filepath.Join(dir, "out"),
		"--namespace", "Bad_Namespace",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "namespace must be a DNS-1123 label") {
		t.Fatalf("expected invalid namespace error, got %v", err)
	}
}

func TestCompileRejectsInvalidProbeImagePullPolicyOverride(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(wd, "..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	var stdout, stderr bytes.Buffer
	err = Run([]string{
		"compile",
		"--scenario", filepath.Join("examples", "scenarios", "mqtt-ingestion-basic.yaml"),
		"--binding", filepath.Join("examples", "bindings", "local-dev.yaml"),
		"--out", filepath.Join(dir, "out"),
		"--probe-image-pull-policy", "Sometimes",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "probe imagePullPolicy must be one of") {
		t.Fatalf("expected imagePullPolicy override error, got %v", err)
	}
}

func TestCompileRejectsMissingProbeImage(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(wd, "..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	bindingPath := filepath.Join(dir, "binding.yaml")
	content, err := os.ReadFile(filepath.Join("examples", "bindings", "local-dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "    image: spex-probe:dev\n", "", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err = Run([]string{
		"compile",
		"--scenario", filepath.Join("examples", "scenarios", "mqtt-ingestion-basic.yaml"),
		"--binding", bindingPath,
		"--out", filepath.Join(dir, "out"),
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "probe image is required") {
		t.Fatalf("expected missing probe image error, got %v", err)
	}
}

func TestCompileOverridesProbeImage(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(wd, "..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	bindingPath := filepath.Join(dir, "binding.yaml")
	bindingContent, err := os.ReadFile(filepath.Join("examples", "bindings", "local-dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	bindingContent = []byte(strings.Replace(string(bindingContent), "    image: spex-probe:dev\n", "", 1))
	if err := os.WriteFile(bindingPath, bindingContent, 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out")
	var stdout, stderr bytes.Buffer
	err = Run([]string{
		"compile",
		"--scenario", filepath.Join("examples", "scenarios", "mqtt-ingestion-basic.yaml"),
		"--binding", bindingPath,
		"--out", out,
		"--run-id", "run-fixed-test",
		"--probe-image", "spex-probe:local",
		"--probe-image-pull-policy", "Never",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "03-op-publish-reading-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`image: "spex-probe:local"`,
		`imagePullPolicy: "Never"`,
	} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("compiled Job missing %q:\n%s", want, string(content))
		}
	}
}

func TestCompileOverridesClusterSettings(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(wd, "..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	out := filepath.Join(dir, "out")
	var stdout, stderr bytes.Buffer
	err = Run([]string{
		"compile",
		"--scenario", filepath.Join("examples", "scenarios", "mqtt-ingestion-basic.yaml"),
		"--binding", filepath.Join("examples", "bindings", "local-dev.yaml"),
		"--out", out,
		"--run-id", "run-fixed-test",
		"--kube-context", "kind-kind",
		"--namespace", "spex-kind",
		"--start-kind",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	kuttlTest, err := os.ReadFile(filepath.Join(out, "kuttl-test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	stepMap, err := os.ReadFile(filepath.Join(out, "step-map.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`namespace: "spex-kind"`,
		"startKIND: true",
		"artifactsDir: artifacts/kuttl",
		"reportFormat: xml",
	} {
		if !strings.Contains(string(kuttlTest), want) {
			t.Fatalf("kuttl-test.yaml missing %q:\n%s", want, string(kuttlTest))
		}
	}
	for _, want := range []string{
		`namespace: "spex-kind"`,
		`kubeContext: "kind-kind"`,
	} {
		if !strings.Contains(string(stepMap), want) {
			t.Fatalf("step-map.yaml missing %q:\n%s", want, string(stepMap))
		}
	}
}

func TestCompileAcceptsIntegrationProfile(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(wd, "..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	out := filepath.Join(dir, "out")
	var stdout, stderr bytes.Buffer
	err = Run([]string{
		"compile",
		"--scenario", filepath.Join("examples", "scenarios", "mqtt-ingestion-basic.yaml"),
		"--binding", filepath.Join("examples", "bindings", "local-dev.yaml"),
		"--integration-profile", filepath.Join("examples", "integration", "local-kind-profile.yaml"),
		"--out", out,
		"--run-id", "run-fixed-test",
		"--kube-context", "kind-kind",
		"--repo-root", "../..",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	kuttlTest, err := os.ReadFile(filepath.Join(out, "kuttl-test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kuttlTest), "kindConfig: ./kind.yaml") || !strings.Contains(string(kuttlTest), "startKIND: true") || !strings.Contains(string(kuttlTest), "\ntimeout: 300\n") || !strings.Contains(string(kuttlTest), "suppress:\n  - events") {
		t.Fatalf("kuttl-test.yaml missing integration profile fields:\n%s", string(kuttlTest))
	}
	if !strings.Contains(string(kuttlTest), "docker build -f ../../examples/integration/probe/Dockerfile") {
		t.Fatalf("kuttl-test.yaml missing rendered repo root override:\n%s", string(kuttlTest))
	}
	if _, err := os.Stat(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "01-integration-setup.yaml")); err != nil {
		t.Fatalf("missing integration setup step: %v", err)
	}
}

func TestValidateAcceptsIntegrationProfile(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(wd, "..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	var stdout, stderr bytes.Buffer
	err = Run([]string{
		"validate",
		"--scenario", filepath.Join("examples", "scenarios", "mqtt-ingestion-basic.yaml"),
		"--binding", filepath.Join("examples", "bindings", "local-dev.yaml"),
		"--integration-profile", filepath.Join("examples", "integration", "local-kind-profile.yaml"),
		"--kube-context", "kind-kind",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "validation passed") {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

func TestValidateRejectsMismatchedIntegrationKINDContext(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(wd, "..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	var stdout, stderr bytes.Buffer
	err = Run([]string{
		"validate",
		"--scenario", filepath.Join("examples", "scenarios", "mqtt-ingestion-basic.yaml"),
		"--binding", filepath.Join("examples", "bindings", "local-dev.yaml"),
		"--integration-profile", filepath.Join("examples", "integration", "local-kind-profile.yaml"),
		"--kube-context", "kind-other",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), `requires kubeContext "kind-kind"`) {
		t.Fatalf("expected mismatched kind context error, got %v", err)
	}
}

func TestValidateRejectsInvalidNamespaceOverride(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(wd, "..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	var stdout, stderr bytes.Buffer
	err = Run([]string{
		"validate",
		"--scenario", filepath.Join("examples", "scenarios", "mqtt-ingestion-basic.yaml"),
		"--binding", filepath.Join("examples", "bindings", "local-dev.yaml"),
		"--namespace", "Bad_Namespace",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "namespace must be a DNS-1123 label") {
		t.Fatalf("expected invalid namespace error, got %v", err)
	}
}

func TestCompileRejectsMismatchedIntegrationKINDContext(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(wd, "..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	var stdout, stderr bytes.Buffer
	err = Run([]string{
		"compile",
		"--scenario", filepath.Join("examples", "scenarios", "mqtt-ingestion-basic.yaml"),
		"--binding", filepath.Join("examples", "bindings", "local-dev.yaml"),
		"--integration-profile", filepath.Join("examples", "integration", "local-kind-profile.yaml"),
		"--out", filepath.Join(dir, "out"),
		"--run-id", "run-fixed-test",
		"--kube-context", "kind-other",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), `requires kubeContext "kind-kind"`) {
		t.Fatalf("expected mismatched kind context error, got %v", err)
	}
}

func TestRunWorkspaceCollectsLogsAndProbeResults(t *testing.T) {
	workspace := writeWorkspace(t)
	logPath := filepath.Join(t.TempDir(), "kubectl-args.log")
	probeResult := `{"apiVersion":"spex.probe.result.v0.1","operation":"mqtt.publish","status":"passed"}`
	fake := writeRecordingKubectlWithOutput(t, logPath, 0, "log before\n"+probeResult+"\n")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"run", "--workspace", workspace, "--command", fake}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	logContent, err := os.ReadFile(filepath.Join(workspace, "evidence", "logs", "03-publish-reading-1.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logContent), probeResult) {
		t.Fatalf("log content missing probe result:\n%s", string(logContent))
	}
	resultContent, err := os.ReadFile(filepath.Join(workspace, "evidence", "results", "03-publish-reading-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(resultContent)) != probeResult {
		t.Fatalf("unexpected result content:\n%s", string(resultContent))
	}
	commandLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"kuttl test --config kuttl-test.yaml",
		"--context local-dev -n spex-test get job spex-mqtt-ingestion-basic-03-publish-reading-1 -o json",
		"--context local-dev -n spex-test logs -l spex/operation-id=publish-reading-1,spex/run-id=run-fixed-test,spex/step-ordinal=03",
		"--context local-dev -n spex-test delete job -l spex/owned=true,spex/scenario=mqtt-ingestion-basic --ignore-not-found=true",
		"--context local-dev -n spex-test delete configmap -l spex/owned=true,spex/scenario=mqtt-ingestion-basic,spex/runtime=true --ignore-not-found=true",
	} {
		if !strings.Contains(string(commandLog), want) {
			t.Fatalf("kubectl invocation missing %q:\n%s", want, string(commandLog))
		}
	}
}

func TestRunWorkspaceCollectsNormalizedProbeResultEnvelope(t *testing.T) {
	workspace := writeWorkspace(t)
	logPath := filepath.Join(t.TempDir(), "kubectl-args.log")
	probeResult := `{"operationId":"publish-reading-1","operationType":"redis.assertValueEquals","provider":"redis","status":"passed","result":{},"evidence":[],"diagnostics":[]}`
	fake := writeRecordingKubectlWithOutput(t, logPath, 0, "log before\n"+probeResult+"\n")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"run", "--workspace", workspace, "--command", fake}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	resultContent, err := os.ReadFile(filepath.Join(workspace, "evidence", "results", "03-publish-reading-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(resultContent)) != probeResult {
		t.Fatalf("unexpected normalized result content:\n%s", string(resultContent))
	}
	report := readReport(t, workspace)
	if !strings.Contains(report, "resultRef: evidence/results/03-publish-reading-1.jsonl") {
		t.Fatalf("report missing normalized result ref:\n%s", report)
	}
}

func TestCollectEvidenceRejectsSymlinkLogFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	workspace := writeWorkspace(t)
	stepMap, err := loadStepMap(workspace)
	if err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(workspace, "evidence", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realLog := filepath.Join(t.TempDir(), "real.log")
	if err := os.WriteFile(realLog, []byte("real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realLog, filepath.Join(logDir, "03-publish-reading-1.log")); err != nil {
		t.Fatal(err)
	}
	fake := writeFakeKubectl(t, 0, "replacement\n")

	collectEvidence(fake, workspace, stepMap)

	content, err := os.ReadFile(realLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "real\n" {
		t.Fatalf("symlink target was modified: %q", string(content))
	}
}

func TestCollectEvidenceRejectsSymlinkEvidenceDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	workspace := writeWorkspace(t)
	stepMap, err := loadStepMap(workspace)
	if err != nil {
		t.Fatal(err)
	}
	evidenceDir := filepath.Join(workspace, "evidence")
	logDir := filepath.Join(evidenceDir, "logs")
	targetDir := t.TempDir()
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetDir, logDir); err != nil {
		t.Fatal(err)
	}
	fake := writeFakeKubectl(t, 0, "replacement\n")

	collectEvidence(fake, workspace, stepMap)

	if _, statErr := os.Stat(filepath.Join(targetDir, "03-publish-reading-1.log")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected symlink target dir to remain untouched, got stat err %v", statErr)
	}
}

func TestRunWorkspaceCollectsResourceUsage(t *testing.T) {
	workspace := writeWorkspace(t)
	logPath := filepath.Join(t.TempDir(), "kubectl-args.log")
	fake := writeRecordingKubectlWithOutput(t, logPath, 0, "NAME CPU(cores) MEMORY(bytes)\nprobe 1m 16Mi\n")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"run", "--workspace", workspace, "--command", fake, "--collect-resource-usage"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	resourceContent, err := os.ReadFile(filepath.Join(workspace, "evidence", "resources", "03-publish-reading-1.pods.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resourceContent), "MEMORY(bytes)") {
		t.Fatalf("resource usage content mismatch:\n%s", string(resourceContent))
	}
	reportContent, err := os.ReadFile(filepath.Join(workspace, "reports", "scenario-run-report.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reportContent), "resourceRef: evidence/resources/03-publish-reading-1.pods.txt") {
		t.Fatalf("report missing resource ref:\n%s", string(reportContent))
	}
	commandLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commandLog), "--context local-dev -n spex-test top pod -l spex/operation-id=publish-reading-1,spex/run-id=run-fixed-test,spex/step-ordinal=03 --containers") {
		t.Fatalf("kubectl top command was not issued:\n%s", string(commandLog))
	}
}

func TestRunBoundedCommandTruncatesOutput(t *testing.T) {
	fake := writeFakeKubectl(t, 0, "abcdef")
	output, err := runBoundedCommand(5, fake, "anything")
	if err != nil {
		t.Fatal(err)
	}
	got := string(output)
	if !strings.HasPrefix(got, "abcde\n[spex: command output truncated after 5 bytes]") {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestRunWorkspaceCanRetainRuntimeResources(t *testing.T) {
	workspace := writeWorkspace(t)
	logPath := filepath.Join(t.TempDir(), "kubectl-args.log")
	fake := writeRecordingKubectl(t, logPath)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"run", "--workspace", workspace, "--command", fake, "--retain-runtime-resources"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	commandLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commandLog), "delete job") || strings.Contains(string(commandLog), "delete configmap") {
		t.Fatalf("retain run deleted runtime resources:\n%s", string(commandLog))
	}
}

func TestRunWorkspaceReportsRuntimeCleanupFailure(t *testing.T) {
	workspace := writeWorkspace(t)
	fake := writeKubectlWithFailingDelete(t)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"run", "--workspace", workspace, "--command", fake}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("expected cleanup failure, got %v", err)
	}
	report := readReport(t, workspace)
	for _, want := range []string{
		"result: error",
		"scenarioResult: passed",
		"runnerResult: error",
		"failureClass: runtime_cleanup_failed",
		"failureMessage: cleanup failed",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestRunWorkspaceTruncatesRuntimeCleanupFailureOutput(t *testing.T) {
	workspace := writeWorkspace(t)
	fake := writeKubectlWithLargeFailingDelete(t, int(maxCleanupOutputSize)+8)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"run", "--workspace", workspace, "--command", fake}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected cleanup failure")
	}
	if !strings.Contains(err.Error(), "command output truncated") {
		t.Fatalf("expected truncation marker in error, got %v", err)
	}
	report := readReport(t, workspace)
	if !strings.Contains(report, "failureClass: runtime_cleanup_failed") || !strings.Contains(report, "command output truncated") {
		t.Fatalf("report missing truncated cleanup failure:\n%s", report)
	}
}

func TestRunWorkspaceWritesNotRunReportWhenKUTTLFailsToStart(t *testing.T) {
	workspace := writeWorkspace(t)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"run", "--workspace", workspace, "--command", filepath.Join(workspace, "missing-kubectl")}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected run error")
	}
	report := readReport(t, workspace)
	for _, want := range []string{
		"result: error",
		"scenarioResult: not_run",
		"runnerResult: error",
		"failureClass: kuttl_execution_failure",
		"result: not_run",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestRunWorkspaceFailsClosedWhenStepMapIsMissing(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := writeFakeKubectl(t, 0, "should not run")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"run", "--workspace", workspace, "--command", fake}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "step-map.yaml") {
		t.Fatalf("expected step-map load error, got %v", err)
	}
	if strings.Contains(stdout.String(), "should not run") {
		t.Fatalf("KUTTL command was executed despite missing step-map: %s", stdout.String())
	}
	report := readReport(t, workspace)
	for _, want := range []string{
		"name: unknown",
		"result: error",
		"scenarioResult: not_run",
		"runnerResult: error",
		"failureClass: workspace_completeness_failure",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestRunWorkspaceMapsKUTTLFailureToStep(t *testing.T) {
	workspace := writeWorkspace(t)
	logPath := filepath.Join(t.TempDir(), "kubectl-args.log")
	fake := writeRecordingKUTTLFailureCleanupSuccess(t, logPath, "job spex-mqtt-ingestion-basic-03-publish-reading-1 failed\n")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"run", "--workspace", workspace, "--command", fake}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected run error")
	}
	report := readReport(t, workspace)
	for _, want := range []string{
		"result: failed",
		"scenarioResult: failed",
		"runnerResult: passed",
		"failureClass: null",
		"operationId: publish-reading-1",
		"failureClass: kuttl_execution_failure",
		"result: failed",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	commandLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--context local-dev -n spex-test delete job -l spex/owned=true,spex/scenario=mqtt-ingestion-basic --ignore-not-found=true",
		"--context local-dev -n spex-test delete configmap -l spex/owned=true,spex/scenario=mqtt-ingestion-basic,spex/runtime=true --ignore-not-found=true",
	} {
		if !strings.Contains(string(commandLog), want) {
			t.Fatalf("failed run cleanup missing %q:\n%s", want, string(commandLog))
		}
	}
}

func TestRunWorkspaceMapsKUTTLApplyFailureByGeneratedFile(t *testing.T) {
	workspace := writeWorkspace(t)
	output := "failed to apply kuttl/mqtt-ingestion-basic/03-op-publish-reading-1.yaml: invalid manifest\n"
	fake := writeKUTTLFailureCleanupSuccess(t, output)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"run", "--workspace", workspace, "--command", fake}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected run error")
	}
	report := readReport(t, workspace)
	for _, want := range []string{
		"operationId: publish-reading-1",
		"failureClass: kuttl_execution_failure",
		"failureMessage: 'failed to apply kuttl/mqtt-ingestion-basic/03-op-publish-reading-1.yaml: invalid manifest'",
		"result: failed",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestReportUsesJobStatusFailureBeforeProbeAndKUTTLText(t *testing.T) {
	workspace := writeWorkspace(t)
	statusDir := filepath.Join(workspace, "evidence", "status")
	resultDir := filepath.Join(workspace, "evidence", "results")
	if err := os.MkdirAll(statusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jobStatus := `{"status":{"conditions":[{"type":"Failed","status":"True","reason":"BackoffLimitExceeded","message":"Job has reached the specified backoff limit"}]}}`
	if err := os.WriteFile(filepath.Join(statusDir, "03-publish-reading-1.job.json"), []byte(jobStatus), 0o644); err != nil {
		t.Fatal(err)
	}
	probeResult := `{"apiVersion":"spex.probe.result.v0.1","operation":"mqtt.publish","status":"failed","failureClass":"mqtt_publish_failed","reason":"publish timeout"}`
	if err := os.WriteFile(filepath.Join(resultDir, "03-publish-reading-1.jsonl"), []byte(probeResult+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := WriteReport(ReportInput{
		Workspace:      workspace,
		StartedAt:      testTime(),
		FinishedAt:     testTime(),
		ScenarioResult: "failed",
		RunnerResult:   "passed",
		KUTTLOutput:    "job spex-mqtt-ingestion-basic-03-publish-reading-1 failed",
	})
	if err != nil {
		t.Fatal(err)
	}
	report := readReport(t, workspace)
	for _, want := range []string{
		"operationId: publish-reading-1",
		"jobStatusRef: evidence/status/03-publish-reading-1.job.json",
		"failureClass: probe_job_failed",
		"failureMessage: Job has reached the specified backoff limit",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "failureMessage: publish timeout") {
		t.Fatalf("probe failure unexpectedly won over job status:\n%s", report)
	}
}

func TestReportUsesProbeResultFailureOverKUTTLOutput(t *testing.T) {
	workspace := writeWorkspace(t)
	resultDir := filepath.Join(workspace, "evidence", "results")
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	probeResult := `{"apiVersion":"spex.probe.result.v0.1","operation":"mqtt.publish","status":"failed","failureClass":"mqtt_publish_failed","reason":"publish timeout"}`
	if err := os.WriteFile(filepath.Join(resultDir, "03-publish-reading-1.jsonl"), []byte(probeResult+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := WriteReport(ReportInput{
		Workspace:      workspace,
		StartedAt:      testTime(),
		FinishedAt:     testTime(),
		ScenarioResult: "passed",
		RunnerResult:   "passed",
		KUTTLOutput:    "",
	})
	if err != nil {
		t.Fatal(err)
	}
	report := readReport(t, workspace)
	for _, want := range []string{
		"result: failed",
		"scenarioResult: failed",
		"failureClass: mqtt_publish_failed",
		"failureMessage: publish timeout",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestReportUsesNormalizedProbeResultEnvelopeFailure(t *testing.T) {
	workspace := writeWorkspace(t)
	resultDir := filepath.Join(workspace, "evidence", "results")
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	probeResult := `{"operationId":"publish-reading-1","operationType":"redis.assertValueEquals","provider":"redis","status":"failed","result":{},"evidence":[],"diagnostics":[{"severity":"error","message":"redis key \"cache:user-123\" value \"pending\" does not equal \"active\""}]}`
	if err := os.WriteFile(filepath.Join(resultDir, "03-publish-reading-1.jsonl"), []byte(probeResult+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := WriteReport(ReportInput{
		Workspace:      workspace,
		StartedAt:      testTime(),
		FinishedAt:     testTime(),
		ScenarioResult: "passed",
		RunnerResult:   "passed",
		KUTTLOutput:    "",
	})
	if err != nil {
		t.Fatal(err)
	}
	report := readReport(t, workspace)
	for _, want := range []string{
		"result: failed",
		"scenarioResult: failed",
		"failureClass: probe_result_failed",
		`failureMessage: redis key "cache:user-123" value "pending" does not equal "active"`,
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestReportIgnoresMalformedNormalizedProbeResultEnvelope(t *testing.T) {
	workspace := writeWorkspace(t)
	resultDir := filepath.Join(workspace, "evidence", "results")
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	probeResult := `{"operationId":"publish-reading-1","operationType":"redis.assertValueEquals","provider":"redis","status":"failed","diagnostics":[{"message":"ignored"}]}`
	if err := os.WriteFile(filepath.Join(resultDir, "03-publish-reading-1.jsonl"), []byte(probeResult+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := WriteReport(ReportInput{
		Workspace:      workspace,
		StartedAt:      testTime(),
		FinishedAt:     testTime(),
		ScenarioResult: "passed",
		RunnerResult:   "passed",
		KUTTLOutput:    "",
	})
	if err != nil {
		t.Fatal(err)
	}
	report := readReport(t, workspace)
	if strings.Contains(report, "probe_result_failed") || strings.Contains(report, "ignored") {
		t.Fatalf("report used malformed normalized probe envelope:\n%s", report)
	}
}

func TestReportIgnoresNormalizedProbeResultWithInvalidProviderResult(t *testing.T) {
	err := validateNormalizedProbeResult(probeResult{
		OperationID:   "publish-reading-1",
		OperationType: "redis.assertValueEquals",
		Provider:      "redis",
		Status:        "passed",
		Result: map[string]any{
			"key": "cache:user-123",
		},
		Evidence:    []probeEvidenceEnvelope{},
		Diagnostics: []probeDiagnostic{},
	}, stepMapStep{OperationID: "publish-reading-1"})
	if err == nil || !strings.Contains(err.Error(), "result.value is required") {
		t.Fatalf("expected provider result schema validation error, got %v", err)
	}
}

func TestReportValidatesNormalizedProbeResultAgainstStepMapResultSchema(t *testing.T) {
	err := validateNormalizedProbeResult(probeResult{
		OperationID:   "echo-message",
		OperationType: "custom.echo",
		Provider:      "custom",
		Status:        "passed",
		Result:        map[string]any{},
		Evidence:      []probeEvidenceEnvelope{},
		Diagnostics:   []probeDiagnostic{},
	}, stepMapStep{
		OperationID: "echo-message",
		ResultSchema: &workspace.JSONSchema{
			Type:     "object",
			Required: []string{"message"},
			Properties: map[string]workspace.JSONSchema{
				"message": {Type: "string"},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "result.message is required") {
		t.Fatalf("expected custom result schema validation error, got %v", err)
	}
}

func TestReportIgnoresSymlinkProbeResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	workspace := writeWorkspace(t)
	resultDir := filepath.Join(workspace, "evidence", "results")
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realResult := filepath.Join(t.TempDir(), "probe.jsonl")
	probeResult := `{"apiVersion":"spex.probe.result.v0.1","operation":"mqtt.publish","status":"failed","failureClass":"mqtt_publish_failed","reason":"publish timeout"}`
	if err := os.WriteFile(realResult, []byte(probeResult+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realResult, filepath.Join(resultDir, "03-publish-reading-1.jsonl")); err != nil {
		t.Fatal(err)
	}

	_, err := WriteReport(ReportInput{
		Workspace:      workspace,
		StartedAt:      testTime(),
		FinishedAt:     testTime(),
		ScenarioResult: "passed",
		RunnerResult:   "passed",
	})
	if err != nil {
		t.Fatal(err)
	}
	report := readReport(t, workspace)
	if strings.Contains(report, "mqtt_publish_failed") || strings.Contains(report, "resultRef: evidence/results/03-publish-reading-1.jsonl") {
		t.Fatalf("report used symlinked probe result:\n%s", report)
	}
}

func TestReportIgnoresOversizedProbeResult(t *testing.T) {
	workspace := writeWorkspace(t)
	resultDir := filepath.Join(workspace, "evidence", "results")
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("x"), int(maxProbeResultFileSize)+1)
	if err := os.WriteFile(filepath.Join(resultDir, "03-publish-reading-1.jsonl"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := WriteReport(ReportInput{
		Workspace:      workspace,
		StartedAt:      testTime(),
		FinishedAt:     testTime(),
		ScenarioResult: "passed",
		RunnerResult:   "passed",
	})
	if err != nil {
		t.Fatal(err)
	}
	report := readReport(t, workspace)
	if strings.Contains(report, "resultRef: evidence/results/03-publish-reading-1.jsonl") {
		t.Fatalf("report referenced oversized probe result:\n%s", report)
	}
}

func TestReportIgnoresSymlinkJobStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	workspace := writeWorkspace(t)
	statusDir := filepath.Join(workspace, "evidence", "status")
	if err := os.MkdirAll(statusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realStatus := filepath.Join(t.TempDir(), "job.json")
	jobStatus := `{"status":{"conditions":[{"type":"Failed","status":"True","reason":"BackoffLimitExceeded","message":"Job has reached the specified backoff limit"}]}}`
	if err := os.WriteFile(realStatus, []byte(jobStatus), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realStatus, filepath.Join(statusDir, "03-publish-reading-1.job.json")); err != nil {
		t.Fatal(err)
	}

	_, err := WriteReport(ReportInput{
		Workspace:      workspace,
		StartedAt:      testTime(),
		FinishedAt:     testTime(),
		ScenarioResult: "failed",
		RunnerResult:   "passed",
		KUTTLOutput:    "job spex-mqtt-ingestion-basic-03-publish-reading-1 failed",
	})
	if err != nil {
		t.Fatal(err)
	}
	report := readReport(t, workspace)
	if strings.Contains(report, "probe_job_failed") || strings.Contains(report, "jobStatusRef: evidence/status/03-publish-reading-1.job.json") {
		t.Fatalf("report used symlinked job status:\n%s", report)
	}
}

func TestReportUsesMissingPodLogForReachedJobWithoutLogs(t *testing.T) {
	workspace := writeWorkspace(t)

	_, err := WriteReport(ReportInput{
		Workspace:      workspace,
		StartedAt:      testTime(),
		FinishedAt:     testTime(),
		ScenarioResult: "failed",
		RunnerResult:   "passed",
		KUTTLOutput:    "job spex-mqtt-ingestion-basic-03-publish-reading-1 failed",
	})
	if err != nil {
		t.Fatal(err)
	}
	report := readReport(t, workspace)
	for _, want := range []string{
		"operationId: publish-reading-1",
		"failureClass: pod_log_collection_missing_pod",
		"failureMessage: KUTTL reported mapped Job failure but no Pod log could be collected for spex-mqtt-ingestion-basic-03-publish-reading-1",
		"operationId: redpanda-snapshot-offsets",
		"result: passed",
		"logRef: null",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestReportKeepsApplyFailureMappedWhenNoPodLogExists(t *testing.T) {
	workspace := writeWorkspace(t)
	output := "failed to apply kuttl/mqtt-ingestion-basic/03-op-publish-reading-1.yaml: invalid manifest\n"

	_, err := WriteReport(ReportInput{
		Workspace:      workspace,
		StartedAt:      testTime(),
		FinishedAt:     testTime(),
		ScenarioResult: "failed",
		RunnerResult:   "passed",
		KUTTLOutput:    output,
	})
	if err != nil {
		t.Fatal(err)
	}
	report := readReport(t, workspace)
	for _, want := range []string{
		"operationId: publish-reading-1",
		"failureClass: kuttl_execution_failure",
		"failureMessage: 'failed to apply kuttl/mqtt-ingestion-basic/03-op-publish-reading-1.yaml: invalid manifest'",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "failureClass: pod_log_collection_missing_pod") {
		t.Fatalf("apply failure should not be reported as missing pod logs:\n%s", report)
	}
}

func TestCleanDeletesRuntimeResources(t *testing.T) {
	workspace := writeWorkspace(t)
	logPath := filepath.Join(t.TempDir(), "kubectl-args.log")
	fake := writeRecordingKubectl(t, logPath)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"clean", "--workspace", workspace, "--command", fake}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--context local-dev -n spex-test delete job -l spex/owned=true,spex/scenario=mqtt-ingestion-basic --ignore-not-found=true",
		"--context local-dev -n spex-test delete configmap -l spex/owned=true,spex/scenario=mqtt-ingestion-basic,spex/runtime=true --ignore-not-found=true",
	} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("clean command missing %q:\n%s", want, string(content))
		}
	}
	if strings.Contains(string(content), "spex/static=true") {
		t.Fatalf("clean without --all deleted static ConfigMaps:\n%s", string(content))
	}
}

func TestCleanAllDeletesStaticConfigMaps(t *testing.T) {
	workspace := writeWorkspace(t)
	logPath := filepath.Join(t.TempDir(), "kubectl-args.log")
	fake := writeRecordingKubectl(t, logPath)

	var stdout, stderr bytes.Buffer
	err := Run([]string{"clean", "--workspace", workspace, "--command", fake, "--all"}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "spex/static=true") {
		t.Fatalf("clean --all did not delete static ConfigMaps:\n%s", string(content))
	}
}

func TestCatalogListAndExplain(t *testing.T) {
	suite := filepath.Join(repoRoot(t), "examples", "suites", "mqtt-local.yaml")

	var listOut, stderr bytes.Buffer
	if err := Run([]string{"catalog", "list", "--suite", suite}, &listOut, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"mqttToRedpandaToGraphql",
		"telemetry-flow.yaml",
		`when "device \"{deviceId}\" publishes energy reading {value:number} as \"{correlationId}\""`,
	} {
		if !strings.Contains(listOut.String(), want) {
			t.Fatalf("catalog list missing %q:\n%s", want, listOut.String())
		}
	}

	var explainOut bytes.Buffer
	if err := Run([]string{"catalog", "explain", "--suite", suite}, &explainOut, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"parameters:",
		"- deviceId: string",
		"payloadTemplates: 1",
		"graphqlQueries: 1",
		"operations: 3",
		`given "tenant \"{tenantId}\"`,
		"operations: 0",
	} {
		if !strings.Contains(explainOut.String(), want) {
			t.Fatalf("catalog explain missing %q:\n%s", want, explainOut.String())
		}
	}

	listOut.Reset()
	if err := Run([]string{"catalog", "list", "--suite", suite, "--format", "json"}, &listOut, &stderr); err != nil {
		t.Fatal(err)
	}
	var listJSON struct {
		Flows []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"flows"`
		Steps []struct {
			Kind       string `json:"kind"`
			Expression string `json:"expression"`
			Source     string `json:"source"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(listOut.Bytes(), &listJSON); err != nil {
		t.Fatalf("catalog list json is invalid: %v\n%s", err, listOut.String())
	}
	if len(listJSON.Flows) != 1 || listJSON.Flows[0].Name != "mqttToRedpandaToGraphql" {
		t.Fatalf("catalog list json flows mismatch:\n%s", listOut.String())
	}
	foundStep := false
	for _, step := range listJSON.Steps {
		if step.Kind == "when" && strings.Contains(step.Expression, "publishes energy reading") {
			foundStep = true
		}
	}
	if !foundStep {
		t.Fatalf("catalog list json missing publish step:\n%s", listOut.String())
	}

	explainOut.Reset()
	if err := Run([]string{"catalog", "explain", "--suite", suite, "--format", "json"}, &explainOut, &stderr); err != nil {
		t.Fatal(err)
	}
	var explainJSON struct {
		Flows []struct {
			Name                 string            `json:"name"`
			Parameters           map[string]string `json:"parameters"`
			PayloadTemplateCount int               `json:"payloadTemplateCount"`
			GraphQLQueryCount    int               `json:"graphqlQueryCount"`
			OperationCount       int               `json:"operationCount"`
		} `json:"flows"`
		Steps []struct {
			Kind                 string `json:"kind"`
			Expression           string `json:"expression"`
			ParameterCount       int    `json:"parameterCount"`
			PayloadTemplateCount int    `json:"payloadTemplateCount"`
			GraphQLQueryCount    int    `json:"graphqlQueryCount"`
			OperationCount       int    `json:"operationCount"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(explainOut.Bytes(), &explainJSON); err != nil {
		t.Fatalf("catalog explain json is invalid: %v\n%s", err, explainOut.String())
	}
	if len(explainJSON.Flows) != 1 || explainJSON.Flows[0].Parameters["deviceId"] != "string" || explainJSON.Flows[0].PayloadTemplateCount != 1 || explainJSON.Flows[0].GraphQLQueryCount != 1 || explainJSON.Flows[0].OperationCount != 3 {
		t.Fatalf("catalog explain json flow mismatch:\n%s", explainOut.String())
	}
	foundThenStep := false
	foundGivenStep := false
	for _, step := range explainJSON.Steps {
		if step.Kind == "then" && strings.Contains(step.Expression, "GraphQL returns") && step.OperationCount == 1 {
			foundThenStep = true
		}
		if step.Kind == "given" && strings.Contains(step.Expression, "tenant") && step.ParameterCount == 1 && step.OperationCount == 0 {
			foundGivenStep = true
		}
	}
	if !foundThenStep {
		t.Fatalf("catalog explain json missing GraphQL step:\n%s", explainOut.String())
	}
	if !foundGivenStep {
		t.Fatalf("catalog explain json missing parameter-only Given step:\n%s", explainOut.String())
	}

	var checkOut bytes.Buffer
	if err := Run([]string{"catalog", "check", "--suite", suite}, &checkOut, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(checkOut.String(), "catalog check passed") {
		t.Fatalf("catalog check output mismatch:\n%s", checkOut.String())
	}
	checkOut.Reset()
	if err := Run([]string{"catalog", "check", "--suite", suite, "--format", "json"}, &checkOut, &stderr); err != nil {
		t.Fatal(err)
	}
	var checkJSON struct {
		Status string   `json:"status"`
		Flows  int      `json:"flows"`
		Steps  int      `json:"steps"`
		Errors []string `json:"failures"`
	}
	if err := json.Unmarshal(checkOut.Bytes(), &checkJSON); err != nil {
		t.Fatalf("catalog check json is invalid: %v\n%s", err, checkOut.String())
	}
	if checkJSON.Status != "passed" || checkJSON.Flows != 1 || checkJSON.Steps != 5 || len(checkJSON.Errors) != 0 {
		t.Fatalf("catalog check json mismatch: %+v\n%s", checkJSON, checkOut.String())
	}

	var docsOut bytes.Buffer
	if err := Run([]string{"catalog", "docs", "--suite", suite}, &docsOut, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# spex Catalog", "mqttToRedpandaToGraphql", "Expansion:", "Payload templates: 1", "Operations: 0", "GraphQL returns"} {
		if !strings.Contains(docsOut.String(), want) {
			t.Fatalf("catalog docs missing %q:\n%s", want, docsOut.String())
		}
	}
}

func TestCatalogDocsRejectsSymlinkOutputFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	suite := filepath.Join(root, "examples", "suites", "mqtt-local.yaml")
	dir := t.TempDir()
	realDoc := filepath.Join(t.TempDir(), "catalog.md")
	if err := os.WriteFile(realDoc, []byte("real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "catalog.md")
	if err := os.Symlink(realDoc, out); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"catalog", "docs", "--suite", suite, "--out", out}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected catalog docs to fail")
	}
	if !strings.Contains(err.Error(), "catalog.md: not a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}
	content, readErr := os.ReadFile(realDoc)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "real\n" {
		t.Fatalf("symlink target was modified: %q", string(content))
	}
}

func TestSuiteList(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	suite := filepath.Join(root, "examples", "suites", "mqtt-local.yaml")

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"suite", "list", "--suite", suite}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"suite: mqtt-local",
		"- mqtt-ingestion-basic",
		"- mqtt-ingestion-flow",
		"- mqtt-reading-reaches-redpanda-and-graphql-1",
		"- mqtt-reading-reaches-redpanda-and-graphql-2",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("suite list missing %q:\n%s", want, stdout.String())
		}
	}

	stdout.Reset()
	if err := Run([]string{"suite", "list", "--suite", suite, "--format", "json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Suite     string `json:"suite"`
		SuiteFile string `json:"suiteFile"`
		Scenarios []struct {
			Name string `json:"name"`
			File string `json:"file"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("suite list json is invalid: %v\n%s", err, stdout.String())
	}
	if parsed.Suite != "mqtt-local" {
		t.Fatalf("suite list json suite = %q", parsed.Suite)
	}
	if parsed.SuiteFile != suite {
		t.Fatalf("suite list json suiteFile = %q, want %q", parsed.SuiteFile, suite)
	}
	if len(parsed.Scenarios) != 5 {
		t.Fatalf("suite list json scenarios = %d, want 5\n%s", len(parsed.Scenarios), stdout.String())
	}
	foundFlow := false
	for _, scenario := range parsed.Scenarios {
		if scenario.Name == "mqtt-ingestion-flow" && strings.HasSuffix(scenario.File, "mqtt-ingestion-flow.yaml") {
			foundFlow = true
		}
	}
	if !foundFlow {
		t.Fatalf("suite list json missing mqtt-ingestion-flow:\n%s", stdout.String())
	}
}

func TestSuitePlan(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	suite := filepath.Join(root, "examples", "suites", "mqtt-local.yaml")

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"suite", "plan", "--suite", suite, "--format", "json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Suite           string `json:"suite"`
		WorkspaceRoot   string `json:"workspaceRoot"`
		RequiredSecrets []struct {
			ID   string   `json:"id"`
			Keys []string `json:"keys"`
		} `json:"requiredSecrets"`
		Providers []struct {
			Provider      string `json:"provider"`
			OperationType string `json:"operationType"`
			BindingKind   string `json:"bindingKind"`
		} `json:"providers"`
		Scenarios []struct {
			Name         string   `json:"name"`
			Operations   []string `json:"operations"`
			Capabilities []struct {
				Provider      string `json:"provider"`
				OperationType string `json:"operationType"`
			} `json:"capabilities"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("suite plan json is invalid: %v\n%s", err, stdout.String())
	}
	if parsed.Suite != "mqtt-local" || parsed.WorkspaceRoot == "" {
		t.Fatalf("suite plan basic fields mismatch:\n%s", stdout.String())
	}
	if len(parsed.Scenarios) != 5 {
		t.Fatalf("suite plan scenarios = %d, want 5\n%s", len(parsed.Scenarios), stdout.String())
	}
	if len(parsed.RequiredSecrets) == 0 {
		t.Fatalf("suite plan missing required secrets:\n%s", stdout.String())
	}
	if !suitePlanHasProvider(parsed.Providers, "mqtt", "mqtt.publish") || !suitePlanHasProvider(parsed.Providers, "graphql", "graphql.expect") {
		t.Fatalf("suite plan missing provider capabilities:\n%s", stdout.String())
	}
	if len(parsed.Scenarios[0].Capabilities) == 0 {
		t.Fatalf("suite plan scenario missing capabilities:\n%s", stdout.String())
	}
}

func TestSuiteExplainJSON(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	suite := filepath.Join(root, "examples", "suites", "mqtt-local.yaml")

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"suite", "explain", "--suite", suite, "--format", "json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Suite     string `json:"suite"`
		SuiteFile string `json:"suiteFile"`
		Providers []struct {
			Provider      string `json:"provider"`
			OperationType string `json:"operationType"`
			BindingKind   string `json:"bindingKind"`
		} `json:"providers"`
		Scenarios []struct {
			Name         string `json:"name"`
			File         string `json:"file"`
			Namespace    string `json:"namespace"`
			Capabilities []struct {
				Provider      string `json:"provider"`
				OperationType string `json:"operationType"`
			} `json:"capabilities"`
			Operations []struct {
				ID                 string `json:"id"`
				Type               string `json:"type"`
				Provider           string `json:"provider"`
				BindingKind        string `json:"bindingKind"`
				BindingName        string `json:"bindingName"`
				Topic              string `json:"topic"`
				PayloadTemplateRef string `json:"payloadTemplateRef"`
				QueryRef           string `json:"queryRef"`
				MatcherCount       int    `json:"matcherCount"`
			} `json:"operations"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("suite explain json is invalid: %v\n%s", err, stdout.String())
	}
	if parsed.Suite != "mqtt-local" || parsed.SuiteFile != suite || len(parsed.Scenarios) != 5 {
		t.Fatalf("suite explain json basic fields mismatch:\n%s", stdout.String())
	}
	if !suitePlanHasProvider(parsed.Providers, "mqtt", "mqtt.publish") || !suitePlanHasProvider(parsed.Providers, "graphql", "graphql.expect") {
		t.Fatalf("suite explain json missing provider capabilities:\n%s", stdout.String())
	}
	foundScenario := false
	for _, scenario := range parsed.Scenarios {
		if scenario.Name != "mqtt-ingestion-basic" {
			continue
		}
		foundScenario = true
		if len(scenario.Capabilities) == 0 {
			t.Fatalf("suite explain json scenario missing capabilities: %+v\n%s", scenario, stdout.String())
		}
		if len(scenario.Operations) != 3 || scenario.Operations[0].Type != "mqtt.publish" || scenario.Operations[0].Provider != "mqtt" || scenario.Operations[0].BindingKind != "mqtt.connection" || scenario.Operations[0].BindingName != "mqtt.default" || scenario.Operations[0].PayloadTemplateRef == "" {
			t.Fatalf("suite explain json operation details mismatch: %+v\n%s", scenario, stdout.String())
		}
	}
	if !foundScenario {
		t.Fatalf("suite explain json missing mqtt-ingestion-basic:\n%s", stdout.String())
	}
}

func TestBundleExplainShowsLocalBundleCapabilities(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bundles", "custom"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bundles", "custom", "catalogs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bundles", "custom", "catalogs", "custom-steps.yaml"), []byte(`apiVersion: spex.catalog.v0.1
kind: StepCatalog
metadata:
  name: custom-steps
spec:
  steps:
    - kind: then
      expression: custom echoes {message}
      output:
        operations:
          - id: echo-{message}
            type: custom.echo
            with:
              bindingRef: custom.main
              message: "{message}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bundles", "custom", "bundle.yaml"), []byte(`apiVersion: spex.bundle.v0.1
kind: IntegrationBundle
metadata:
  name: custom
  version: 0.1.0
spec:
  capabilities:
    - type: custom.echo
      bindingKind: custom.connection
      inputSchema:
        schema:
          type: object
      resultSchema:
        schema:
          type: object
      probe:
        image: custom-probe:dev
        command: ["custom", "run"]
        env:
          CUSTOM_TOKEN:
            secretRef: credentials.token
          CUSTOM_URI:
            fromBinding: uri
        input:
          mode: operationFile
          path: /custom/input/operation.json
        output:
          path: /custom/output/result.json
  bindingSchemas:
    - kind: custom.connection
  stepCatalogs:
    - catalogs/custom-steps.yaml
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "suite.yaml"), []byte(`apiVersion: spex.suite.v0.1
kind: ScenarioSuite
metadata:
  name: custom-suite
spec:
  bindingRef: binding.yaml
  bundleRefs:
    - name: custom
      version: 0.1.0
      source: bundles/custom
  scenarios:
    - scenario.yaml
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(`apiVersion: spex.scenario.v0.1
kind: Scenario
metadata:
  name: custom-check
spec:
  operations:
    - id: echo-message
      type: custom.echo
      with:
        bindingRef: custom.main
        message: hello
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "binding.yaml"), []byte(`apiVersion: spex.binding.v0.1
kind: TargetBinding
metadata:
  name: local
spec:
  namespace: spex-test
  rbac:
    create: true
  bindings:
    - name: custom.main
      kind: custom.connection
      with:
        uri: custom://service
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"bundle", "explain", "--suite", filepath.Join(dir, "suite.yaml")}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"custom",
		"version: 0.1.0",
		"source: bundles/custom",
		"sourceType: local",
		"manifest:",
		"custom-steps.yaml",
		"custom.echo",
		"custom.connection",
		"inputSchema: true",
		"inputSchemaRef: inline",
		"resultSchema: true",
		"resultSchemaRef: inline",
		"custom-probe:dev",
		"/custom/input/operation.json",
		"/custom/output/result.json",
		"CUSTOM_TOKEN: secretRef:credentials.token",
		"CUSTOM_URI: fromBinding:uri",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("bundle explain missing %q:\n%s", want, stdout.String())
		}
	}
	stdout.Reset()
	if err := Run([]string{"bundle", "explain", "--suite", filepath.Join(dir, "suite.yaml"), "--format", "json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Bundles []struct {
			Name         string   `json:"name"`
			Version      string   `json:"version"`
			Source       string   `json:"source"`
			SourceType   string   `json:"sourceType"`
			ManifestFile string   `json:"manifestFile"`
			CatalogFiles []string `json:"catalogFiles"`
			Capabilities []struct {
				Type            string            `json:"type"`
				InputSchema     bool              `json:"inputSchema"`
				InputSchemaRef  string            `json:"inputSchemaRef"`
				ResultSchema    bool              `json:"resultSchema"`
				ResultSchemaRef string            `json:"resultSchemaRef"`
				Env             map[string]string `json:"env"`
			} `json:"capabilities"`
		} `json:"bundles"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("bundle explain json is invalid: %v\n%s", err, stdout.String())
	}
	if len(parsed.Bundles) != 1 || parsed.Bundles[0].Name != "custom" || parsed.Bundles[0].Version != "0.1.0" || parsed.Bundles[0].Source != "bundles/custom" || parsed.Bundles[0].SourceType != "local" || parsed.Bundles[0].ManifestFile == "" || len(parsed.Bundles[0].CatalogFiles) != 1 || len(parsed.Bundles[0].Capabilities) != 1 {
		t.Fatalf("bundle explain json shape mismatch:\n%s", stdout.String())
	}
	capability := parsed.Bundles[0].Capabilities[0]
	if capability.Type != "custom.echo" || !capability.InputSchema || capability.InputSchemaRef != "inline" || !capability.ResultSchema || capability.ResultSchemaRef != "inline" || capability.Env["CUSTOM_TOKEN"] != "secretRef:credentials.token" || capability.Env["CUSTOM_URI"] != "fromBinding:uri" {
		t.Fatalf("bundle explain json capability mismatch: %+v\n%s", capability, stdout.String())
	}
}

func TestBundleExplainShowsBuiltInBundleCapabilities(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "suite.yaml"), []byte(`apiVersion: spex.suite.v0.1
kind: ScenarioSuite
metadata:
  name: redis-suite
spec:
  bindingRef: binding.yaml
  bundleRefs:
    - name: redis
      version: 0.1.0
      source: builtin:redis
  scenarios:
    - scenario.yaml
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(`apiVersion: spex.scenario.v0.1
kind: Scenario
metadata:
  name: redis-check
spec:
  operations:
    - id: assert-cache-value
      type: redis.assertValueEquals
      with:
        bindingRef: redis.main
        key: cache:user-123
        equals: active
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "binding.yaml"), []byte(`apiVersion: spex.binding.v0.1
kind: TargetBinding
metadata:
  name: local
spec:
  namespace: spex-test
  rbac:
    create: true
  bindings:
    - name: redis.main
      kind: redis.connection
      with:
        uri: redis://redis.default.svc.cluster.local:6379/0
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"bundle", "explain", "--suite", filepath.Join(dir, "suite.yaml")}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"redis",
		"redis.assertValueEquals",
		"redis.connection",
		"spex-probe:dev",
		"/spex/input/operation.json",
		"/spex/output/result.json",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("bundle explain missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestSuiteValidateLoadsBundleStepCatalogs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bundles", "custom", "catalogs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bundles", "custom", "bundle.yaml"), []byte(`apiVersion: spex.bundle.v0.1
kind: IntegrationBundle
metadata:
  name: custom
  version: 0.1.0
spec:
  capabilities:
    - type: custom.echo
      bindingKind: custom.connection
      inputSchema:
        schema:
          type: object
          required:
            - message
          properties:
            message:
              type: string
      resultSchema:
        schema:
          type: object
      probe:
        image: custom-probe:dev
        command: ["custom", "run"]
        input:
          mode: operationFile
          path: /custom/input/operation.json
        output:
          path: /custom/output/result.json
  bindingSchemas:
    - kind: custom.connection
  stepCatalogs:
    - catalogs/custom-steps.yaml
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bundles", "custom", "catalogs", "custom-steps.yaml"), []byte(`apiVersion: spex.catalog.v0.1
kind: StepCatalog
metadata:
  name: custom-steps
spec:
  steps:
    - kind: then
      expression: custom echoes {message}
      output:
        operations:
          - id: echo-{message}
            type: custom.echo
            with:
              bindingRef: custom.main
              message: "{message}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "suite.yaml"), []byte(`apiVersion: spex.suite.v0.1
kind: ScenarioSuite
metadata:
  name: custom-suite
spec:
  bindingRef: binding.yaml
  bundleRefs:
    - name: custom
      version: 0.1.0
      source: bundles/custom
  scenarios:
    - scenario.yaml
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(`apiVersion: spex.scenario.v0.1
kind: Scenario
metadata:
  name: custom-check
spec:
  stepInvocations:
    - kind: then
      text: custom echoes hello
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "binding.yaml"), []byte(`apiVersion: spex.binding.v0.1
kind: TargetBinding
metadata:
  name: local
spec:
  namespace: spex-test
  rbac:
    create: true
  probe:
    image: spex-probe:dev
  bindings:
    - name: custom.main
      kind: custom.connection
      with:
        uri: custom://service
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"suite", "validate", "--suite", filepath.Join(dir, "suite.yaml")}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "suite validation passed: 1 scenario(s)") {
		t.Fatalf("suite validate output mismatch:\n%s", stdout.String())
	}

	stdout.Reset()
	if err := Run([]string{"suite", "explain", "--suite", filepath.Join(dir, "suite.yaml"), "--format", "json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		CatalogFiles []string `json:"catalogFiles"`
		Scenarios    []struct {
			Operations []struct {
				ID          string `json:"id"`
				Type        string `json:"type"`
				Provider    string `json:"provider"`
				BindingName string `json:"bindingName"`
			} `json:"operations"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("suite explain json is invalid: %v\n%s", err, stdout.String())
	}
	if len(parsed.CatalogFiles) != 1 || !strings.HasSuffix(parsed.CatalogFiles[0], filepath.Join("bundles", "custom", "catalogs", "custom-steps.yaml")) {
		t.Fatalf("bundle catalog path missing from suite explain: %+v\n%s", parsed.CatalogFiles, stdout.String())
	}
	if len(parsed.Scenarios) != 1 || len(parsed.Scenarios[0].Operations) != 1 {
		t.Fatalf("bundle step catalog did not expand operation:\n%s", stdout.String())
	}
	operation := parsed.Scenarios[0].Operations[0]
	if operation.ID != "echo-hello" || operation.Type != "custom.echo" || operation.Provider != "custom" || operation.BindingName != "custom.main" {
		t.Fatalf("expanded operation mismatch: %+v\n%s", operation, stdout.String())
	}
}

func TestSuiteValidateUsesBundleSchemaFileRefs(t *testing.T) {
	dir := t.TempDir()
	bundleDir := filepath.Join(dir, "bundles", "custom")
	if err := os.MkdirAll(filepath.Join(bundleDir, "schemas"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "schemas", "custom-input.schema.yaml"), []byte(`type: object
required:
  - message
properties:
  message:
    type: string
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "schemas", "custom-result.schema.yaml"), []byte(`type: object
required:
  - echoed
properties:
  echoed:
    type: string
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "schemas", "custom-binding.schema.yaml"), []byte(`type: object
required:
  - endpoint
properties:
  endpoint:
    type: string
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "bundle.yaml"), []byte(`apiVersion: spex.bundle.v0.1
kind: IntegrationBundle
metadata:
  name: custom
  version: 0.1.0
spec:
  capabilities:
    - type: custom.echo
      bindingKind: custom.connection
      inputSchema:
        path: schemas/custom-input.schema.yaml
      resultSchema:
        path: schemas/custom-result.schema.yaml
      probe:
        image: custom-probe:dev
        command: ["custom", "run"]
        input:
          mode: operationFile
          path: /custom/input/operation.json
        output:
          path: /custom/output/result.json
  bindingSchemas:
    - kind: custom.connection
      schema:
        path: schemas/custom-binding.schema.yaml
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "suite.yaml"), []byte(`apiVersion: spex.suite.v0.1
kind: ScenarioSuite
metadata:
  name: custom-suite
spec:
  bindingRef: binding.yaml
  bundleRefs:
    - name: custom
      version: 0.1.0
      source: bundles/custom
  scenarios:
    - scenario.yaml
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(`apiVersion: spex.scenario.v0.1
kind: Scenario
metadata:
  name: custom-check
spec:
  operations:
    - id: echo-message
      type: custom.echo
      with:
        bindingRef: custom.main
        message: hello
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "binding.yaml"), []byte(`apiVersion: spex.binding.v0.1
kind: TargetBinding
metadata:
  name: local
spec:
  namespace: spex-test
  rbac:
    create: true
  probe:
    image: spex-probe:dev
  bindings:
    - name: custom.main
      kind: custom.connection
      with:
        endpoint: custom://service
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"suite", "validate", "--suite", filepath.Join(dir, "suite.yaml")}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}

	scenarioWithoutMessage := strings.Replace(readTestFile(t, filepath.Join(dir, "scenario.yaml")), "        message: hello\n", "", 1)
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(scenarioWithoutMessage), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Run([]string{"suite", "validate", "--suite", filepath.Join(dir, "suite.yaml")}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "custom.echo input schema validation failed") || !strings.Contains(err.Error(), "with.message is required") {
		t.Fatalf("expected input schema validation error, got %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(strings.Replace(scenarioWithoutMessage, "        bindingRef: custom.main\n", "        bindingRef: custom.main\n        message: hello\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	bindingWithoutEndpoint := strings.Replace(readTestFile(t, filepath.Join(dir, "binding.yaml")), "        endpoint: custom://service\n", "", 1)
	if err := os.WriteFile(filepath.Join(dir, "binding.yaml"), []byte(bindingWithoutEndpoint), 0o644); err != nil {
		t.Fatal(err)
	}
	err = Run([]string{"suite", "validate", "--suite", filepath.Join(dir, "suite.yaml")}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), `binding "custom.main" schema validation failed`) || !strings.Contains(err.Error(), "binding.custom.main.with.endpoint is required") {
		t.Fatalf("expected binding schema validation error, got %v", err)
	}
}

func TestSuiteValidateRejectsUnknownBuiltInBundleRef(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "suite.yaml"), []byte(`apiVersion: spex.suite.v0.1
kind: ScenarioSuite
metadata:
  name: unknown-builtin-suite
spec:
  bindingRef: binding.yaml
  bundleRefs:
    - name: does-not-exist
      source: builtin:does-not-exist
  scenarios:
    - scenario.yaml
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(`apiVersion: spex.scenario.v0.1
kind: Scenario
metadata:
  name: redis-check
spec:
  operations:
    - id: assert-cache-value
      type: redis.assertValueEquals
      with:
        bindingRef: redis.main
        key: cache:user-123
        equals: active
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "binding.yaml"), []byte(`apiVersion: spex.binding.v0.1
kind: TargetBinding
metadata:
  name: local
spec:
  namespace: spex-test
  rbac:
    create: true
  bindings:
    - name: redis.main
      kind: redis.connection
      with:
        uri: redis://redis.default.svc.cluster.local:6379/0
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := Run([]string{"suite", "validate", "--suite", filepath.Join(dir, "suite.yaml")}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), `unknown built-in provider "does-not-exist"`) {
		t.Fatalf("expected unknown built-in provider error, got %v", err)
	}
}

func suitePlanHasProvider(providers []struct {
	Provider      string `json:"provider"`
	OperationType string `json:"operationType"`
	BindingKind   string `json:"bindingKind"`
}, provider, operationType string) bool {
	for _, item := range providers {
		if item.Provider == provider && item.OperationType == operationType {
			return true
		}
	}
	return false
}

func TestDoctorWithSuiteJSON(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	suite := filepath.Join(root, "examples", "suites", "mqtt-local.yaml")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"doctor", "--suite", suite, "--format", "json"}, &stdout, &stderr)
	var parsed struct {
		Status string `json:"status"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if jsonErr := json.Unmarshal(stdout.Bytes(), &parsed); jsonErr != nil {
		t.Fatalf("doctor json is invalid: %v\n%s", jsonErr, stdout.String())
	}
	if len(parsed.Checks) == 0 {
		t.Fatalf("doctor returned no checks:\n%s", stdout.String())
	}
	if err != nil && ExitCode(err) != ExitPreflight {
		t.Fatalf("doctor error has wrong exit code: %v", err)
	}
}

func TestDoctorCanSkipHostToolChecks(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	suite := filepath.Join(root, "examples", "suites", "mqtt-local.yaml")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"doctor", "--suite", suite, "--skip-host-tools", "--format", "json"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("doctor skip host tools returned error: %v\n%s", err, stdout.String())
	}
	var parsed doctorOutput
	if jsonErr := json.Unmarshal(stdout.Bytes(), &parsed); jsonErr != nil {
		t.Fatalf("doctor json is invalid: %v\n%s", jsonErr, stdout.String())
	}
	if !hasDoctorCheck(parsed.Checks, "tool:host", "skipped") {
		t.Fatalf("expected skipped host tool check, got %+v", parsed.Checks)
	}
	for _, check := range parsed.Checks {
		if strings.HasPrefix(check.Name, "tool:docker") || strings.HasPrefix(check.Name, "tool:kind") {
			t.Fatalf("host tool check was not skipped: %+v", check)
		}
	}
}

func TestDoctorSecretMaterializationChecksLocalEnvFile(t *testing.T) {
	dir := t.TempDir()
	bindingPath := filepath.Join(dir, "binding.yaml")
	envFile := filepath.Join(dir, "local.env")
	if err := os.WriteFile(envFile, []byte("MQTT_USERNAME=user\nMQTT_PASSWORD=pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checks := secretMaterializationChecks([]workspace.Inputs{{
		BindingPath: bindingPath,
		Binding: workspace.TargetBinding{Spec: workspace.BindingSpec{Secrets: map[string]workspace.Secret{
			"mqtt-credentials": {
				Type:    "localEnvFile",
				Name:    "mqtt-credentials",
				EnvFile: "local.env",
				Env:     map[string]string{"username": "MQTT_USERNAME", "password": "MQTT_PASSWORD"},
				Keys:    map[string]string{"username": "username", "password": "password"},
			},
		}}},
	}})
	if !hasDoctorCheck(checks, "secret:mqtt-credentials:localEnvFile", "passed") {
		t.Fatalf("expected passing localEnvFile check, got %+v", checks)
	}

	checks = secretMaterializationChecks([]workspace.Inputs{{
		BindingPath: bindingPath,
		Binding: workspace.TargetBinding{Spec: workspace.BindingSpec{Secrets: map[string]workspace.Secret{
			"mqtt-credentials": {
				Type:    "localEnvFile",
				Name:    "mqtt-credentials",
				EnvFile: "missing.env",
				Keys:    map[string]string{"username": "username", "password": "password"},
			},
		}}},
	}})
	if !hasDoctorCheck(checks, "secret:mqtt-credentials:localEnvFile", "failed") {
		t.Fatalf("expected failed localEnvFile check, got %+v", checks)
	}
}

func TestDoctorSecretMaterializationWarnsForMissingLocalEnvNames(t *testing.T) {
	dir := t.TempDir()
	bindingPath := filepath.Join(dir, "binding.yaml")
	envFile := filepath.Join(dir, "local.env")
	if err := os.WriteFile(envFile, []byte("MQTT_USERNAME=user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checks := secretMaterializationChecks([]workspace.Inputs{{
		BindingPath: bindingPath,
		Binding: workspace.TargetBinding{Spec: workspace.BindingSpec{Secrets: map[string]workspace.Secret{
			"mqtt-credentials": {
				Type:    "localEnvFile",
				Name:    "mqtt-credentials",
				EnvFile: "local.env",
				Env:     map[string]string{"username": "MQTT_USERNAME", "password": "MQTT_PASSWORD"},
				Keys:    map[string]string{"username": "username", "password": "password"},
			},
		}}},
	}})
	if !hasDoctorCheck(checks, "secret:mqtt-credentials:env:MQTT_PASSWORD", "warning") {
		t.Fatalf("expected missing env warning, got %+v", checks)
	}
}

func TestDoctorSecretMaterializationChecksSSMNeedsAWS(t *testing.T) {
	checks := secretMaterializationChecks([]workspace.Inputs{{
		Binding: workspace.TargetBinding{Spec: workspace.BindingSpec{Secrets: map[string]workspace.Secret{
			"mqtt-credentials": {
				Type:          "awsSsmParameter",
				Name:          "mqtt-credentials",
				Keys:          map[string]string{"username": "username", "password": "password"},
				SSMParameters: map[string]string{"username": `{{ ssm "team/dev/mqtt/username" }}`, "password": "/team/dev/mqtt/password"},
			},
		}}},
	}})
	if !hasDoctorCheckName(checks, "tool:aws") {
		t.Fatalf("expected aws tool check, got %+v", checks)
	}
}

func TestDoctorWarnsForMutableExternalRefs(t *testing.T) {
	warnings := mutableExternalRefWarnings(workspace.ScenarioSuite{
		Spec: workspace.ScenarioSuiteSpec{
			BindingRef:            "git::ssh://git.example/platform-targets.git//bindings/local-kind.yaml@main",
			IntegrationProfileRef: "team/platform-targets/integration/local-kind.yaml@v1.2.3",
			CatalogRefs: []string{
				"team/catalogs/telemetry-steps.yaml@master",
				"team/catalogs/telemetry-flow.yaml@0f1e2d3c4b",
			},
		},
	})
	if len(warnings) != 2 {
		t.Fatalf("expected two mutable-ref warnings, got %d: %+v", len(warnings), warnings)
	}
	for _, want := range []string{"@main", "@master"} {
		if !containsString(warnings, want) {
			t.Fatalf("expected warning containing %q, got %+v", want, warnings)
		}
	}
	for _, notWant := range []string{"@v1.2.3", "@0f1e2d3c4b"} {
		if containsString(warnings, notWant) {
			t.Fatalf("unexpected warning containing pinned ref %q: %+v", notWant, warnings)
		}
	}
}

func TestDoctorArtifactSecretScanFailsOnLeakedEnvValue(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPEX_MQTT_PASSWORD", "super-secret-password")
	if err := os.WriteFile(filepath.Join(dir, "probe.log"), []byte("password=super-secret-password\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"doctor", "--scan-artifacts", dir, "--format", "json"}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected artifact secret scan failure, got success:\n%s", stdout.String())
	}
	if ExitCode(err) != ExitPreflight {
		t.Fatalf("expected preflight exit code, got %d for %v", ExitCode(err), err)
	}
	var parsed doctorOutput
	if jsonErr := json.Unmarshal(stdout.Bytes(), &parsed); jsonErr != nil {
		t.Fatalf("doctor json is invalid: %v\n%s", jsonErr, stdout.String())
	}
	if parsed.Status != "failed" || !hasDoctorCheck(parsed.Checks, "artifactSecretScan:SPEX_MQTT_PASSWORD", "failed") {
		t.Fatalf("expected failed artifact secret scan check, got %+v\n%s", parsed, stdout.String())
	}
	if strings.Contains(stdout.String(), "super-secret-password") {
		t.Fatalf("doctor output leaked secret value:\n%s", stdout.String())
	}
}

func TestDoctorArtifactSecretScanPassesWithoutLeak(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPEX_GRAPHQL_TOKEN", "safe-token-value")
	if err := os.WriteFile(filepath.Join(dir, "report.json"), []byte(`{"token":"$SPEX_GRAPHQL_TOKEN"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	checks := artifactSecretScanChecks([]string{dir}, []string{"SPEX_GRAPHQL_TOKEN"})
	if !hasDoctorCheck(checks, "artifactSecretScan:"+dir, "passed") {
		t.Fatalf("expected passed artifact scan, got %+v", checks)
	}
}

func TestDoctorArtifactScanFailsOnKubeconfigFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "live"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "live", "kubeconfig"), []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"doctor", "--scan-artifacts", dir, "--format", "json"}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected kubeconfig artifact scan failure, got success:\n%s", stdout.String())
	}
	if ExitCode(err) != ExitPreflight {
		t.Fatalf("expected preflight exit code, got %d for %v", ExitCode(err), err)
	}
	var parsed doctorOutput
	if jsonErr := json.Unmarshal(stdout.Bytes(), &parsed); jsonErr != nil {
		t.Fatalf("doctor json is invalid: %v\n%s", jsonErr, stdout.String())
	}
	if parsed.Status != "failed" || !hasDoctorCheck(parsed.Checks, "artifactKubeconfigScan", "failed") {
		t.Fatalf("expected failed kubeconfig artifact scan check, got %+v\n%s", parsed, stdout.String())
	}
}

func TestDoctorArtifactScanFailsOnKubeconfigContent(t *testing.T) {
	dir := t.TempDir()
	content := []byte("apiVersion: v1\nclusters: []\ncontexts: []\nusers: []\n")
	if err := os.WriteFile(filepath.Join(dir, "admin.conf"), content, 0o600); err != nil {
		t.Fatal(err)
	}

	checks := artifactSecretScanChecks([]string{dir}, nil)
	if !hasDoctorCheck(checks, "artifactKubeconfigScan", "failed") {
		t.Fatalf("expected kubeconfig-shaped content failure, got %+v", checks)
	}
}

func TestDoctorArtifactSecretScanFailsMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	checks := artifactSecretScanChecks([]string{missing}, nil)
	if !hasDoctorCheck(checks, "artifactSecretScan:"+missing, "failed") {
		t.Fatalf("expected missing artifact scan path failure, got %+v", checks)
	}
}

func TestDoctorArtifactSecretScanRejectsSymlinkRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	target := t.TempDir()
	root := filepath.Join(t.TempDir(), "artifacts")
	if err := os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}

	checks := artifactSecretScanChecks([]string{root}, nil)
	if !hasDoctorCheck(checks, "artifactSecretScan:"+root, "failed") {
		t.Fatalf("expected symlink root failure, got %+v", checks)
	}
}

func TestDoctorArtifactSecretScanSkipsSymlinkFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := t.TempDir()
	t.Setenv("SPEX_GRAPHQL_TOKEN", "secret-token-value")
	realFile := filepath.Join(t.TempDir(), "real.log")
	if err := os.WriteFile(realFile, []byte("token=secret-token-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realFile, filepath.Join(dir, "linked.log")); err != nil {
		t.Fatal(err)
	}

	checks := artifactSecretScanChecks([]string{dir}, []string{"SPEX_GRAPHQL_TOKEN"})
	if !hasDoctorCheck(checks, "artifactSecretScan:"+dir, "passed") {
		t.Fatalf("expected symlink file to be skipped, got %+v", checks)
	}
}

func TestDoctorPinnedImageChecksRequireDigest(t *testing.T) {
	inputs := []workspace.Inputs{
		{
			ScenarioName: "tagged",
			Binding: workspace.TargetBinding{Spec: workspace.BindingSpec{Probe: workspace.Probe{
				Image: "registry.example.com/spex-probe:1.2.3",
			}}},
		},
		{
			ScenarioName: "pinned",
			Binding: workspace.TargetBinding{Spec: workspace.BindingSpec{Probe: workspace.Probe{
				Image: "registry.example.com/spex-probe@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			}}},
		},
		{
			ScenarioName: "bad-digest",
			Binding: workspace.TargetBinding{Spec: workspace.BindingSpec{Probe: workspace.Probe{
				Image: "registry.example.com/spex-probe@sha256:not-a-digest",
			}}},
		},
	}

	checks := pinnedImageChecks(inputs)
	if !hasDoctorCheck(checks, "imageRef:tagged:probe", "failed") {
		t.Fatalf("expected tagged probe image failure, got %+v", checks)
	}
	if !hasDoctorCheck(checks, "imageRef:pinned:probe", "passed") {
		t.Fatalf("expected pinned probe image pass, got %+v", checks)
	}
	if !hasDoctorCheck(checks, "imageRef:bad-digest:probe", "failed") {
		t.Fatalf("expected invalid digest failure, got %+v", checks)
	}
}

func TestCatalogExpressionsCanOverlap(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{
			name: "number also matches untyped placeholder",
			a:    `device "{deviceId}" publishes value {value:number}`,
			b:    `device "{deviceId}" publishes value {payload}`,
			want: true,
		},
		{
			name: "broad expression can consume literal boundary",
			a:    `{subject} publishes`,
			b:    `{subject} {verb}`,
			want: true,
		},
		{
			name: "different literals do not overlap",
			a:    `Redpanda contains reading "{correlationId}"`,
			b:    `GraphQL returns reading "{correlationId}"`,
			want: false,
		},
		{
			name: "number does not match nonnumeric literal",
			a:    `value {value:number}`,
			b:    `value text`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := catalogExpressionsCanOverlap(tc.a, tc.b); got != tc.want {
				t.Fatalf("catalogExpressionsCanOverlap(%q, %q)=%v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestCatalogCheckRejectsAmbiguousStepExpressions(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "steps.yaml")
	content := `apiVersion: spex.catalog.v0.1
kind: StepCatalog
metadata:
  name: ambiguous-steps
spec:
  steps:
    - kind: when
      expression: 'device "{deviceId}" publishes value {value:number}'
      output:
        parameters:
          value:
            type: string
            default: "{value}"
    - kind: when
      expression: 'device "{deviceId}" publishes value {payload}'
      output:
        parameters:
          payload:
            type: string
            default: "{payload}"
`
	if err := os.WriteFile(catalogPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"catalog", "check", "--catalog", catalogPath}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected catalog check failure, got success:\n%s", stdout.String())
	}
	if ExitCode(err) != ExitValidation {
		t.Fatalf("expected validation exit code, got %d for %v", ExitCode(err), err)
	}
	if !strings.Contains(stdout.String(), "ambiguous step expressions") {
		t.Fatalf("expected ambiguity output, got:\n%s", stdout.String())
	}

	stdout.Reset()
	err = Run([]string{"catalog", "check", "--catalog", catalogPath, "--format", "json"}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected catalog check json failure, got success:\n%s", stdout.String())
	}
	if ExitCode(err) != ExitValidation {
		t.Fatalf("expected validation exit code for json, got %d for %v", ExitCode(err), err)
	}
	var parsed struct {
		Status   string   `json:"status"`
		Failures []string `json:"failures"`
	}
	if jsonErr := json.Unmarshal(stdout.Bytes(), &parsed); jsonErr != nil {
		t.Fatalf("catalog check failure json is invalid: %v\n%s", jsonErr, stdout.String())
	}
	if parsed.Status != "failed" || len(parsed.Failures) == 0 || !strings.Contains(parsed.Failures[0], "ambiguous step expressions") {
		t.Fatalf("catalog check failure json mismatch: %+v\n%s", parsed, stdout.String())
	}
}

func hasDoctorCheck(checks []doctorCheck, name, status string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == status {
			return true
		}
	}
	return false
}

func hasDoctorCheckName(checks []doctorCheck, name string) bool {
	for _, check := range checks {
		if check.Name == name {
			return true
		}
	}
	return false
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func TestSuiteRunWritesJUnitReport(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	suite := filepath.Join(root, "examples", "suites", "mqtt-local.yaml")
	out := filepath.Join(t.TempDir(), "suite-run")
	fake := writeFakeKubectl(t, 0, "")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"suite", "run", "--suite", suite, "--out", out, "--run-id", "suite-test", "--command", fake}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(out, "reports", "suite-junit.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "reports", "suite-run-report.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "mqtt-ingestion-basic", "reports", "scenario-run-report.json")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<testsuites tests="5" failures="0">`,
		`<testcase name="mqtt-ingestion-basic" classname="spex.scenario"></testcase>`,
		`<testcase name="mqtt-reading-reaches-redpanda-and-graphql-2" classname="spex.scenario"></testcase>`,
		`<testcase name="mqtt-ingestion-steps" classname="spex.scenario"></testcase>`,
	} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("suite JUnit missing %q:\n%s", want, string(content))
		}
	}
	yamlContent, err := os.ReadFile(filepath.Join(out, "reports", "suite-run-report.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"kind: SuiteRunReport",
		"result: passed",
		"tests: 5",
		"name: mqtt-ingestion-basic",
	} {
		if !strings.Contains(string(yamlContent), want) {
			t.Fatalf("suite YAML missing %q:\n%s", want, string(yamlContent))
		}
	}
}

func TestSuiteRunHonorsConfiguredReportDirWithoutOutOverride(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acceptance-tests")
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"init", "scenario-repo", "--dir", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	fake := writeFakeKubectl(t, 0, "")
	err := Run([]string{"suite", "run", "--suite", filepath.Join(dir, "suite.yaml"), "--run-id", "suite-test", "--command", fake}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "reports", "suite-junit.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), `<testsuites tests="3" failures="0">`) {
		t.Fatalf("suite JUnit did not include scaffold scenarios:\n%s", string(content))
	}
	if _, err := os.Stat(filepath.Join(dir, "reports", "suite-run-report.yaml")); err != nil {
		t.Fatalf("expected suite YAML report in configured report dir: %v", err)
	}
}

func TestSuiteRunNormalizesExistingReportFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX file modes")
	}
	dir := filepath.Join(t.TempDir(), "acceptance-tests")
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"init", "scenario-repo", "--dir", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(dir, "reports")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(reportDir, "suite-run-report.yaml")
	if err := os.WriteFile(reportPath, []byte("stale\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := writeFakeKubectl(t, 0, "")
	if err := Run([]string{"suite", "run", "--suite", filepath.Join(dir, "suite.yaml"), "--run-id", "suite-test", "--command", fake}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	assertFileMode(t, reportPath, 0o644)
}

func TestSuiteRunRejectsSymlinkReportFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := filepath.Join(t.TempDir(), "acceptance-tests")
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"init", "scenario-repo", "--dir", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(dir, "reports")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realReport := filepath.Join(t.TempDir(), "suite-run-report.yaml")
	if err := os.WriteFile(realReport, []byte("real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realReport, filepath.Join(reportDir, "suite-run-report.yaml")); err != nil {
		t.Fatal(err)
	}
	fake := writeFakeKubectl(t, 0, "")
	err := Run([]string{"suite", "run", "--suite", filepath.Join(dir, "suite.yaml"), "--run-id", "suite-test", "--command", fake}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected suite run to fail")
	}
	if !strings.Contains(err.Error(), "suite-run-report.yaml: not a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}
	content, readErr := os.ReadFile(realReport)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "real\n" {
		t.Fatalf("symlink target was modified: %q", string(content))
	}
}

func TestSuiteRunRejectsSymlinkReportDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := filepath.Join(t.TempDir(), "acceptance-tests")
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"init", "scenario-repo", "--dir", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(dir, "reports")
	targetDir := t.TempDir()
	if err := os.RemoveAll(reportDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetDir, reportDir); err != nil {
		t.Fatal(err)
	}
	fake := writeFakeKubectl(t, 0, "")
	err := Run([]string{"suite", "run", "--suite", filepath.Join(dir, "suite.yaml"), "--run-id", "suite-test", "--command", fake}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected suite run to fail")
	}
	if !strings.Contains(err.Error(), "refusing symlink directory") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(targetDir, "suite-run-report.yaml")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected symlink target dir to remain untouched, got stat err %v", statErr)
	}
}

func TestReadReportSummaryRejectsSymlinkScenarioReport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := t.TempDir()
	realReport := filepath.Join(t.TempDir(), "scenario-run-report.yaml")
	if err := os.WriteFile(realReport, []byte("name: real\nresult: passed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(dir, "scenario-run-report.yaml")
	if err := os.Symlink(realReport, reportPath); err != nil {
		t.Fatal(err)
	}

	name, status, message := readReportSummary(reportPath)
	if name != "" || status != "error" || !strings.Contains(message, "scenario-run-report.yaml: not a regular file") {
		t.Fatalf("unexpected summary: name=%q status=%q message=%q", name, status, message)
	}
}

func TestReadReportSummaryRejectsOversizedScenarioReport(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "scenario-run-report.yaml")
	content := bytes.Repeat([]byte("x"), int(maxScenarioReportSummarySize)+1)
	if err := os.WriteFile(reportPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	name, status, message := readReportSummary(reportPath)
	if name != "" || status != "error" || !strings.Contains(message, "scenario-run-report.yaml: file is too large") {
		t.Fatalf("unexpected summary: name=%q status=%q message=%q", name, status, message)
	}
}

func TestReadReportSummaryParsesStrictScenarioReport(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "scenario-run-report.yaml")
	content := `apiVersion: spex.report.v0.1
kind: ScenarioRunReport
metadata:
  name: strict-report
status:
  result: failed
  scenarioResult: failed
  runnerResult: passed
  startedAt: "2026-05-31T00:00:00Z"
  finishedAt: "2026-05-31T00:00:01Z"
  failureMessage: explicit failure
spec:
  workspace: /tmp/workspace
steps: []
`
	if err := os.WriteFile(reportPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	name, status, message := readReportSummary(reportPath)
	if name != "strict-report" || status != "failed" || message != "explicit failure" {
		t.Fatalf("unexpected summary: name=%q status=%q message=%q", name, status, message)
	}
}

func TestReadReportSummaryRejectsUnknownScenarioReportField(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "scenario-run-report.yaml")
	content := `apiVersion: spex.report.v0.1
kind: ScenarioRunReport
metadata:
  name: strict-report
status:
  result: passed
spec:
  workspace: /tmp/workspace
steps: []
unexpected: true
`
	if err := os.WriteFile(reportPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	name, status, message := readReportSummary(reportPath)
	if name != "" || status != "error" || !strings.Contains(message, "field unexpected not found") {
		t.Fatalf("unexpected summary: name=%q status=%q message=%q", name, status, message)
	}
}

func TestReadReportSummaryRejectsTrailingScenarioReportDocument(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "scenario-run-report.yaml")
	content := `apiVersion: spex.report.v0.1
kind: ScenarioRunReport
metadata:
  name: strict-report
status:
  result: passed
spec:
  workspace: /tmp/workspace
steps: []
---
{}
`
	if err := os.WriteFile(reportPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	name, status, message := readReportSummary(reportPath)
	if name != "" || status != "error" || !strings.Contains(message, "unexpected trailing YAML document") {
		t.Fatalf("unexpected summary: name=%q status=%q message=%q", name, status, message)
	}
}

func TestLimitedCaptureTruncatesWithoutShortWrite(t *testing.T) {
	capture := newLimitedCapture(5)
	n, err := capture.Write([]byte("abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("expected full write acknowledgement, got %d", n)
	}
	got := capture.String()
	if !strings.HasPrefix(got, "abcde\n[spex: command output truncated after 5 bytes]") {
		t.Fatalf("unexpected captured output: %q", got)
	}
}

func TestSuiteRunSkipsJUnitWhenReportFormatExcludesIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acceptance-tests")
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"init", "scenario-repo", "--dir", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	suitePath := filepath.Join(dir, "suite.yaml")
	content, err := os.ReadFile(suitePath)
	if err != nil {
		t.Fatal(err)
	}
	content = bytes.ReplaceAll(content, []byte("      - junit\n"), nil)
	if err := os.WriteFile(suitePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	fake := writeFakeKubectl(t, 0, "")
	err = Run([]string{"suite", "run", "--suite", suitePath, "--run-id", "suite-test", "--command", fake}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "reports", "suite-junit.xml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no suite-junit.xml when junit format is excluded, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "reports", "suite-run-report.yaml")); err != nil {
		t.Fatalf("expected YAML report when yaml format remains enabled: %v", err)
	}
}

func TestSuiteValidateRejectsDuplicateReportFormats(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acceptance-tests")
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"init", "scenario-repo", "--dir", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	suitePath := filepath.Join(dir, "suite.yaml")
	content, err := os.ReadFile(suitePath)
	if err != nil {
		t.Fatal(err)
	}
	content = bytes.Replace(content, []byte("      - junit\n"), []byte("      - junit\n      - junit\n"), 1)
	if err := os.WriteFile(suitePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	err = Run([]string{"suite", "validate", "--suite", suitePath}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), `spec.reports.format contains duplicate format "junit"`) {
		t.Fatalf("expected duplicate report format error, got %v", err)
	}
}

func TestSuiteRunWritesReportsOnFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acceptance-tests")
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"init", "scenario-repo", "--dir", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	fake := writeKUTTLFailureCleanupSuccess(t, "job publish-reading-1 failed\n")
	err := Run([]string{"suite", "run", "--suite", filepath.Join(dir, "suite.yaml"), "--run-id", "suite-test", "--command", fake}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "suite failed") {
		t.Fatalf("expected suite failure, got %v", err)
	}

	yamlContent, err := os.ReadFile(filepath.Join(dir, "reports", "suite-run-report.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"result: failed",
		"failures: 3",
		"failureMessage:",
	} {
		if !strings.Contains(string(yamlContent), want) {
			t.Fatalf("failed suite YAML missing %q:\n%s", want, string(yamlContent))
		}
	}
	junitContent, err := os.ReadFile(filepath.Join(dir, "reports", "suite-junit.xml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<testsuites tests="3" failures="3">`,
		`<failure message=`,
	} {
		if !strings.Contains(string(junitContent), want) {
			t.Fatalf("failed suite JUnit missing %q:\n%s", want, string(junitContent))
		}
	}
}

func TestSchemaListAndShow(t *testing.T) {
	var listOut, stderr bytes.Buffer
	if err := Run([]string{"schema", "list"}, &listOut, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"scenario",
		"scenario-suite",
		"target-binding",
		"integration-profile",
		"integration-bundle",
		"flow-catalog",
		"step-catalog",
	} {
		if !strings.Contains(listOut.String(), want) {
			t.Fatalf("schema list missing %q:\n%s", want, listOut.String())
		}
	}

	var listJSON bytes.Buffer
	if err := Run([]string{"schema", "list", "--format", "json"}, &listJSON, &stderr); err != nil {
		t.Fatal(err)
	}
	var parsedList struct {
		Schemas []string `json:"schemas"`
	}
	if err := json.Unmarshal(listJSON.Bytes(), &parsedList); err != nil {
		t.Fatalf("schema list json is invalid: %v\n%s", err, listJSON.String())
	}
	for _, want := range []string{"scenario", "scenario-suite", "target-binding", "integration-bundle"} {
		found := false
		for _, got := range parsedList.Schemas {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("schema list json missing %q:\n%s", want, listJSON.String())
		}
	}

	for _, name := range strings.Fields(listOut.String()) {
		var schemaOut bytes.Buffer
		if err := Run([]string{"schema", "show", name}, &schemaOut, &stderr); err != nil {
			t.Fatal(err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(schemaOut.Bytes(), &parsed); err != nil {
			t.Fatalf("schema %s is not valid JSON: %v\n%s", name, err, schemaOut.String())
		}
		if parsed["$schema"] == "" || parsed["title"] == "" {
			t.Fatalf("schema %s missing metadata:\n%s", name, schemaOut.String())
		}
		if name == "scenario-suite" {
			for _, want := range []string{`"uniqueItems": true`, `"minLength": 1`} {
				if !strings.Contains(schemaOut.String(), want) {
					t.Fatalf("scenario-suite schema missing %s:\n%s", want, schemaOut.String())
				}
			}
		}
		if name == "scenario" {
			for _, want := range []string{`"minItems": 1`, `"const": "string"`, `"with"`, `"dependsOn"`, `^[a-z][a-z0-9-]*\\.[A-Za-z][A-Za-z0-9_.-]*$`} {
				if !strings.Contains(schemaOut.String(), want) {
					t.Fatalf("scenario schema missing %s:\n%s", want, schemaOut.String())
				}
			}
		}
		if name == "target-binding" {
			for _, want := range []string{`"minProperties": 1`, `"minLength": 1`, `"bindings"`, `"genericBinding"`} {
				if !strings.Contains(schemaOut.String(), want) {
					t.Fatalf("target-binding schema missing %s:\n%s", want, schemaOut.String())
				}
			}
		}
		if name == "integration-profile" {
			for _, want := range []string{`"minimum": 0`, `"propertyNames"`} {
				if !strings.Contains(schemaOut.String(), want) {
					t.Fatalf("integration-profile schema missing %s:\n%s", want, schemaOut.String())
				}
			}
		}
		if name == "integration-bundle" {
			for _, want := range []string{`"IntegrationBundle"`, `"capabilities"`, `"probeInvocation"`, `"qualifiedName"`, `"probeEnvSource"`, `"oneOf"`, `"propertyNames"`} {
				if !strings.Contains(schemaOut.String(), want) {
					t.Fatalf("integration-bundle schema missing %s:\n%s", want, schemaOut.String())
				}
			}
		}
		if name == "flow-catalog" {
			for _, want := range []string{`"minProperties": 1`, `"minItems": 1`} {
				if !strings.Contains(schemaOut.String(), want) {
					t.Fatalf("flow-catalog schema missing %s:\n%s", want, schemaOut.String())
				}
			}
		}
		if name == "step-catalog" {
			for _, want := range []string{`"and"`, `"minItems": 1`, `"minLength": 1`, `"anyOf"`} {
				if !strings.Contains(schemaOut.String(), want) {
					t.Fatalf("step-catalog schema missing %s:\n%s", want, schemaOut.String())
				}
			}
		}
	}
}

func TestHelpCommand(t *testing.T) {
	for _, args := range [][]string{
		{"help"},
		{"--help"},
		{"-h"},
	} {
		var stdout, stderr bytes.Buffer
		if err := Run(args, &stdout, &stderr); err != nil {
			t.Fatalf("Run(%v) returned error: %v", args, err)
		}
		for _, want := range []string{"usage: spex <command> [flags]", "suite", "catalog", "init scenario-repo"} {
			if !strings.Contains(stdout.String(), want) {
				t.Fatalf("Run(%v) help missing %q:\n%s", args, want, stdout.String())
			}
		}
		if stderr.Len() != 0 {
			t.Fatalf("Run(%v) wrote stderr:\n%s", args, stderr.String())
		}
	}
}

func TestNestedHelpCommands(t *testing.T) {
	tests := []struct {
		args []string
		want []string
	}{
		{args: []string{"suite"}, want: []string{"usage: spex suite <command>", "validate", "plan", "run"}},
		{args: []string{"suite", "help"}, want: []string{"usage: spex suite <command>", "compile"}},
		{args: []string{"catalog"}, want: []string{"usage: spex catalog <command>", "check", "docs"}},
		{args: []string{"catalog", "--help"}, want: []string{"usage: spex catalog <command>", "explain"}},
		{args: []string{"schema"}, want: []string{"usage: spex schema <command>", "list", "show"}},
		{args: []string{"schema", "-h"}, want: []string{"usage: spex schema <command>", "scenario-suite"}},
	}
	for _, tc := range tests {
		var stdout, stderr bytes.Buffer
		if err := Run(tc.args, &stdout, &stderr); err != nil {
			t.Fatalf("Run(%v) returned error: %v", tc.args, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(stdout.String(), want) {
				t.Fatalf("Run(%v) help missing %q:\n%s", tc.args, want, stdout.String())
			}
		}
		if stderr.Len() != 0 {
			t.Fatalf("Run(%v) wrote stderr:\n%s", tc.args, stderr.String())
		}
	}
}

func TestRejectsUnexpectedPositionalArgs(t *testing.T) {
	tests := [][]string{
		{"help", "extra"},
		{"version", "extra"},
		{"validate", "extra"},
		{"compile", "extra"},
		{"suite", "help", "extra"},
		{"suite", "list", "extra"},
		{"suite", "plan", "extra"},
		{"catalog", "help", "extra"},
		{"catalog", "list", "extra"},
		{"catalog", "docs", "extra"},
		{"schema", "help", "extra"},
		{"doctor", "extra"},
		{"init", "scenario-repo", "extra"},
		{"run", "extra"},
		{"clean", "extra"},
	}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		err := Run(args, &stdout, &stderr)
		if err == nil {
			t.Fatalf("Run(%v) succeeded; expected positional argument error", args)
		}
		if !strings.Contains(err.Error(), "does not accept positional arguments") {
			t.Fatalf("Run(%v) error mismatch: %v", args, err)
		}
	}
}

func TestNewScenarioRejectsAmbiguousNames(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acceptance-tests")
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"init", "scenario-repo", "--dir", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"new", "scenario", "--dir", dir, "--name", "from-flag", "from-positional"},
		{"new", "scenario", "--dir", dir, "one", "two"},
	} {
		stdout.Reset()
		err := Run(args, &stdout, &stderr)
		if err == nil {
			t.Fatalf("Run(%v) succeeded; expected ambiguous name error", args)
		}
		if !strings.Contains(err.Error(), "positional scenario name") && !strings.Contains(err.Error(), "either --name or one positional") {
			t.Fatalf("Run(%v) error mismatch: %v", args, err)
		}
	}
}

func TestScaffoldCommandsRejectEmptyDirectory(t *testing.T) {
	tests := [][]string{
		{"init", "scenario-repo", "--dir", ""},
		{"new", "scenario", "--dir", "", "--name", "empty-dir"},
	}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		err := Run(args, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "requires a non-empty --dir") {
			t.Fatalf("Run(%v) error mismatch: %v", args, err)
		}
	}
}

func TestInitScenarioRepoWritesEditorSchemas(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acceptance-tests")

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"init", "scenario-repo", "--dir", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run([]string{"init", "scenario-repo", "--dir", dir}, &stdout, &stderr); err == nil {
		t.Fatal("expected second init to refuse existing files")
	} else if !strings.Contains(err.Error(), "refusing to overwrite existing file") {
		t.Fatalf("second init error mismatch: %v", err)
	}
	for _, path := range []string{
		filepath.Join(dir, ".gitignore"),
		filepath.Join(dir, "Makefile"),
		filepath.Join(dir, "README.md"),
		filepath.Join(dir, "ci", "spex-validate.sh"),
		filepath.Join(dir, ".github", "workflows", "spex.yaml"),
		filepath.Join(dir, "suite.yaml"),
		filepath.Join(dir, ".vscode", "settings.json"),
		filepath.Join(dir, ".schemas", "scenario.schema.json"),
		filepath.Join(dir, ".schemas", "scenario-suite.schema.json"),
		filepath.Join(dir, ".schemas", "target-binding.schema.json"),
		filepath.Join(dir, "catalogs", "telemetry-flow.yaml"),
		filepath.Join(dir, "catalogs", "telemetry-steps.yaml"),
		filepath.Join(dir, "features", "mqtt-ingestion.feature"),
		filepath.Join(dir, "scenarios", "mqtt-ingestion-flow.yaml"),
		filepath.Join(dir, "integration"),
		filepath.Join(dir, "catalogs"),
		filepath.Join(dir, "features"),
		filepath.Join(dir, "ci"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected scaffold path %s: %v", path, err)
		}
	}
	scriptInfo, err := os.Stat(filepath.Join(dir, "ci", "spex-validate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if scriptInfo.Mode()&0o111 == 0 {
		t.Fatalf("expected CI script to be executable, mode=%s", scriptInfo.Mode())
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"empty refs",
		"empty matcher arrays",
		"duplicate report formats",
		"empty secret key maps",
		"External references",
		"platform-targets/bindings/dev.yaml@v1.2.3",
		"Custom applications",
		"helmApps:",
		"image.digest",
	} {
		if !strings.Contains(string(readme), want) {
			t.Fatalf("scenario repo README missing %q:\n%s", want, string(readme))
		}
	}
	settings, err := os.ReadFile(filepath.Join(dir, ".vscode", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), ".schemas/scenario.schema.json") {
		t.Fatalf("VS Code settings missing schema mapping:\n%s", string(settings))
	}
	makefile, err := os.ReadFile(filepath.Join(dir, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"help:",
		"spex scenario repository targets:",
		"validate:",
		"$(SPEX) suite validate --suite $(SUITE)",
		"doctor-json:",
		"$(SPEX) doctor --suite $(SUITE) --format json > reports/doctor.json",
		"production-check:",
		"REQUIRE_PINNED_IMAGES",
		"--skip-host-tools",
		"--require-pinned-git-refs",
		"--require-pinned-images",
		"--scan-artifacts",
		"ci:",
		"./ci/spex-validate.sh",
		"schemas:",
		"$(SPEX) schema show scenario-suite",
	} {
		if !strings.Contains(string(makefile), want) {
			t.Fatalf("scenario repo Makefile missing %q:\n%s", want, string(makefile))
		}
	}
	workflow, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "spex.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workflow), "./ci/spex-validate.sh") {
		t.Fatalf("workflow missing validation script:\n%s", string(workflow))
	}
	ciScript, err := os.ReadFile(filepath.Join(dir, "ci", "spex-validate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ciScript), `schema list --format json > reports/schema-list.json`) {
		t.Fatalf("CI script missing schema inventory artifact:\n%s", string(ciScript))
	}
	for _, want := range []string{
		"SPEX_PRODUCTION_CHECK",
		"SPEX_REQUIRE_PINNED_IMAGES",
		"reports/production-check.json",
		"--require-pinned-git-refs",
		"--require-pinned-images",
		`--scan-artifacts "$OUT"`,
	} {
		if !strings.Contains(string(ciScript), want) {
			t.Fatalf("CI script missing production gate fragment %q:\n%s", want, string(ciScript))
		}
	}
	for _, want := range []string{
		"permissions:",
		"contents: read",
		"concurrency:",
		"cancel-in-progress: true",
		"timeout-minutes: 15",
		"persist-credentials: false",
		"actions/upload-artifact@v4",
		"name: spex-reports",
		"path: reports/",
		"retention-days: 14",
		"Production gate",
		"Live jobs should remove generated kubeconfigs before uploading artifacts",
		"find generated -name kubeconfig -type f -delete",
		"representative SPEX_*",
		"secret environment variables",
	} {
		if !strings.Contains(string(workflow), want) {
			t.Fatalf("workflow missing %q:\n%s", want, string(workflow))
		}
	}
	suite, err := os.ReadFile(filepath.Join(dir, "suite.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"catalogRefs:",
		"catalogs/telemetry-flow.yaml",
		"features/**/*.feature",
		"Each listed format must be unique.",
	} {
		if !strings.Contains(string(suite), want) {
			t.Fatalf("suite missing %q:\n%s", want, string(suite))
		}
	}

	err = Run([]string{"init", "scenario-repo", "--dir", dir}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("expected non-destructive init error, got %v", err)
	}
}

func TestInitScenarioRepoRejectsSymlinkScaffoldDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := filepath.Join(t.TempDir(), "acceptance-tests")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	targetDir := t.TempDir()
	if err := os.Symlink(targetDir, filepath.Join(dir, "scenarios")); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"init", "scenario-repo", "--dir", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected init to fail")
	}
	if !strings.Contains(err.Error(), "refusing symlink directory") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(targetDir, "mqtt-ingestion-basic.yaml")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected symlink target dir to remain untouched, got stat err %v", statErr)
	}
}

func TestInitScenarioRepoRejectsSymlinkScaffoldFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := filepath.Join(t.TempDir(), "acceptance-tests")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	realReadme := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(realReadme, []byte("real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realReadme, filepath.Join(dir, "README.md")); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"init", "scenario-repo", "--dir", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected init to fail")
	}
	if !strings.Contains(err.Error(), "path is a symlink") {
		t.Fatalf("unexpected error: %v", err)
	}
	content, readErr := os.ReadFile(realReadme)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "real\n" {
		t.Fatalf("symlink target was modified: %q", string(content))
	}
}

func TestNewScenarioStyles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acceptance-tests")
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"init", "scenario-repo", "--dir", dir}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		path string
		want string
	}{
		{
			name: "explicit",
			args: []string{"new", "scenario", "--dir", dir, "--name", "explicit check"},
			path: filepath.Join(dir, "scenarios", "explicit-check.yaml"),
			want: "operations:",
		},
		{
			name: "flow",
			args: []string{"new", "scenario", "--dir", dir, "--name", "flow check", "--style", "flow"},
			path: filepath.Join(dir, "scenarios", "flow-check.yaml"),
			want: "flow: mqttToRedpandaToGraphql",
		},
		{
			name: "feature",
			args: []string{"new", "scenario", "--dir", dir, "--name", "feature check", "--style", "feature"},
			path: filepath.Join(dir, "features", "feature-check.feature"),
			want: "Scenario: feature check",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Run(tc.args, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(content), tc.want) {
				t.Fatalf("new scenario file missing %q:\n%s", tc.want, string(content))
			}
		})
	}

	if err := Run([]string{"suite", "validate", "--suite", filepath.Join(dir, "suite.yaml")}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	err := Run([]string{"new", "scenario", "--dir", dir, "--name", "bad style", "--style", "gherkin"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unsupported scenario style") {
		t.Fatalf("expected unsupported style error, got %v", err)
	}
}

func TestNewScenarioRejectsSymlinkScenarioDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := filepath.Join(t.TempDir(), "acceptance-tests")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	targetDir := t.TempDir()
	if err := os.Symlink(targetDir, filepath.Join(dir, "scenarios")); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run([]string{"new", "scenario", "--dir", dir, "--name", "leaky scenario"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected new scenario to fail")
	}
	if !strings.Contains(err.Error(), "refusing symlink directory") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(targetDir, "leaky-scenario.yaml")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected symlink target dir to remain untouched, got stat err %v", statErr)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func chdir(t *testing.T, dir string) func() {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	}
}

func writeWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	stepMap := `apiVersion: spex.stepmap.v0.1
kind: StepMap
metadata:
  scenario: mqtt-ingestion-basic
  runId: run-fixed-test
spec:
  scenarioFile: examples/scenarios/mqtt-ingestion-basic.yaml
  bindingFile: examples/bindings/local-dev.yaml
  namespace: spex-test
  kubeContext: local-dev
  steps:
    - ordinal: 2
      operationId: redpanda-snapshot-offsets
      operationType: redpanda.snapshotOffsets
      jobName: spex-mqtt-ingestion-basic-02-redpanda-snapshot-offsets
      podSelector:
        spex/run-id: run-fixed-test
        spex/operation-id: redpanda-snapshot-offsets
        spex/step-ordinal: "02"
      generatedFiles:
        - kuttl/mqtt-ingestion-basic/02-op-redpanda-snapshot-offsets.yaml
        - kuttl/mqtt-ingestion-basic/02-assert.yaml
    - ordinal: 3
      operationId: publish-reading-1
      operationType: mqtt.publish
      jobName: spex-mqtt-ingestion-basic-03-publish-reading-1
      podSelector:
        spex/run-id: run-fixed-test
        spex/operation-id: publish-reading-1
        spex/step-ordinal: "03"
      generatedFiles:
        - kuttl/mqtt-ingestion-basic/03-op-publish-reading-1.yaml
        - kuttl/mqtt-ingestion-basic/03-assert.yaml
`
	if err := os.WriteFile(filepath.Join(dir, "step-map.yaml"), []byte(stepMap), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFakeCommand(t *testing.T, exitCode int) string {
	return writeFakeKubectl(t, exitCode, "")
}

func writeFakeKubectl(t *testing.T, exitCode int, output string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubectl")
	content := "#!/bin/sh\n"
	if output != "" {
		content += "printf '%s' " + shellQuote(output) + "\n"
	}
	content += "exit " + string(rune('0'+exitCode)) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeRecordingKubectl(t *testing.T, logPath string) string {
	return writeRecordingKubectlWithOutput(t, logPath, 0, "")
}

func writeRecordingKubectlWithOutput(t *testing.T, logPath string, exitCode int, output string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubectl")
	content := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\n"
	if output != "" {
		content += "printf '%s' " + shellQuote(output) + "\n"
	}
	content += "exit " + string(rune('0'+exitCode)) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeKubectlWithFailingDelete(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubectl")
	content := `#!/bin/sh
case "$*" in
  *"delete job"*)
    printf '%s' 'cleanup failed'
    exit 1
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeKubectlWithLargeFailingDelete(t *testing.T, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubectl")
	content := `#!/bin/sh
case "$*" in
  *"delete job"*)
    yes x | head -c ` + fmt.Sprintf("%d", size) + `
    exit 1
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeKUTTLFailureCleanupSuccess(t *testing.T, output string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubectl")
	content := "#!/bin/sh\ncase \"$*\" in\n"
	content += "  *\"kuttl test\"*)\n"
	content += "    printf '%s' " + shellQuote(output) + "\n"
	content += "    exit 1\n"
	content += "    ;;\n"
	content += "  *)\n"
	content += "    exit 0\n"
	content += "    ;;\n"
	content += "esac\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeRecordingKUTTLFailureCleanupSuccess(t *testing.T, logPath, output string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubectl")
	content := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\ncase \"$*\" in\n"
	content += "  *\"kuttl test\"*)\n"
	content += "    printf '%s' " + shellQuote(output) + "\n"
	content += "    exit 1\n"
	content += "    ;;\n"
	content += "  *)\n"
	content += "    exit 0\n"
	content += "    ;;\n"
	content += "esac\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func writeFakeReleaseDir(t *testing.T, version, commit, buildDate string) string {
	t.Helper()
	dir := t.TempDir()
	writeFakeReleaseDirAt(t, dir, version, commit, buildDate)
	return dir
}

func writeReleaseArtifactPlaceholders(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range releaseArtifacts() {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, expectedReleaseArchiveMode(name)); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFakeReleaseDirAt(t *testing.T, dir, version, commit, buildDate string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	versionJSON := fmt.Sprintf(`{
  "version": %q,
  "buildCommit": %q,
  "buildDate": %q,
  "goVersion": %q,
  "goos": %q,
  "goarch": %q
}
`, version, commit, buildDate, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	binary := "#!/bin/sh\nif [ \"$1\" = \"version\" ] && [ \"$2\" = \"--format\" ] && [ \"$3\" = \"json\" ]; then\ncat <<'JSON'\n" + versionJSON + "JSON\nexit 0\nfi\nexit 2\n"
	files := map[string]string{
		"spex":                   binary,
		"spex-probe":             binary,
		"spex-probe-influxdb":    binary,
		"spex-probe-redis":       binary,
		"spex-demo-stack":        binary,
		"LICENSE":                "Internal Business Source Available License 1.0\n",
		"COMMERCIAL.md":          "# Commercial Licensing\n",
		"CONTRIBUTING.md":        "# Contributing\n",
		"THIRD-PARTY-NOTICES.md": "# Third-Party Notices\n",
		"go-modules.txt":         modulePath + "\ngithub.com/eclipse/paho.mqtt.golang v1.5.1\n",
		"dependency-inventory.json": `{
  "apiVersion": "spex.dependencies.v0.1",
  "kind": "GoModuleInventory",
  "modulePath": "` + modulePath + `",
  "modules": [
    {
      "path": "` + modulePath + `",
      "main": true,
      "raw": "` + modulePath + `"
    },
    {
      "path": "github.com/eclipse/paho.mqtt.golang",
      "version": "v1.5.1",
      "raw": "github.com/eclipse/paho.mqtt.golang v1.5.1"
    }
  ]
}
`,
		"buildinfo.txt":            "spex\n\tpath\t" + modulePath + "\n\tmod\t" + modulePath + "\t(devel)\t\n",
		"third-party-licenses.txt": "# third-party licenses\n\nmodule github.com/eclipse/paho.mqtt.golang v1.5.1\nlicense-file LICENSE\n",
		"release-provenance.json": fmt.Sprintf(`{
  "apiVersion": "spex.provenance.v0.1",
  "kind": "ReleaseProvenance",
  "version": %q,
  "buildCommit": %q,
  "buildDate": %q,
  "goos": %q,
  "goarch": %q,
  "goVersion": %q,
  "modulePath": %q
}
`, version, commit, buildDate, runtime.GOOS, runtime.GOARCH, runtime.Version(), modulePath),
		"version.json": versionJSON,
	}
	for name, content := range files {
		mode := os.FileMode(0o644)
		if expectedReleaseArchiveMode(name)&0o111 != 0 {
			mode = 0o755
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	var sums strings.Builder
	var manifest strings.Builder
	manifest.WriteString("apiVersion: spex.release.v0.1\n")
	manifest.WriteString("version: " + version + "\n")
	manifest.WriteString("buildCommit: " + commit + "\n")
	manifest.WriteString("buildDate: " + buildDate + "\n")
	manifest.WriteString("goos: " + runtime.GOOS + "\n")
	manifest.WriteString("goarch: " + runtime.GOARCH + "\n")
	manifest.WriteString("artifacts:\n")
	for _, name := range releaseArtifacts() {
		sum, err := fileSHA256(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		sums.WriteString(sum + "  " + name + "\n")
		manifest.WriteString("  - path: " + name + "\n")
		manifest.WriteString("    sha256: " + sum + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "SHA256SUMS"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release-manifest.yaml"), []byte(manifest.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "release-manifest.yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func releaseArchiveNameForVersion(version string) string {
	return fmt.Sprintf("spex_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
}

func updateReleaseChecksumsAndManifest(t *testing.T, dir string) {
	t.Helper()
	writeReleaseManifestWithArtifactOrder(t, dir, releaseArtifacts())
}

func writeReleaseChecksumsWithOrder(t *testing.T, dir string, artifactOrder []string) {
	t.Helper()
	var sums strings.Builder
	for _, name := range artifactOrder {
		sum, err := fileSHA256(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		sums.WriteString(sum + "  " + name + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "SHA256SUMS"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeReleaseManifestWithArtifactOrder(t *testing.T, dir string, artifactOrder []string) {
	t.Helper()
	var sums strings.Builder
	var manifest strings.Builder
	manifest.WriteString("apiVersion: spex.release.v0.1\n")
	manifest.WriteString("version: 1.2.3\n")
	manifest.WriteString("buildCommit: abc123\n")
	manifest.WriteString("buildDate: 2026-05-31T00:00:00Z\n")
	manifest.WriteString("goos: " + runtime.GOOS + "\n")
	manifest.WriteString("goarch: " + runtime.GOARCH + "\n")
	manifest.WriteString("artifacts:\n")
	for _, name := range artifactOrder {
		sum, err := fileSHA256(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		sums.WriteString(sum + "  " + name + "\n")
		manifest.WriteString("  - path: " + name + "\n")
		manifest.WriteString("    sha256: " + sum + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "SHA256SUMS"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release-manifest.yaml"), []byte(manifest.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "release-manifest.yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFakeReleaseArchive(t *testing.T, dir, archivePath string) {
	t.Helper()
	writeFakeReleaseArchiveWithOverrides(t, dir, archivePath, nil)
}

func writeFakeReleaseArchiveWithOverrides(t *testing.T, dir, archivePath string, overrides map[string]string) {
	t.Helper()
	writeFakeReleaseArchiveCustom(t, dir, archivePath, overrides, nil, nil, nil, releaseArchiveTimestamp(), releaseArchiveFiles())
}

func writeFakeReleaseArchiveWithModes(t *testing.T, dir, archivePath string, modes map[string]int64) {
	t.Helper()
	writeFakeReleaseArchiveCustom(t, dir, archivePath, nil, nil, modes, nil, releaseArchiveTimestamp(), releaseArchiveFiles())
}

func writeFakeReleaseArchiveWithModTimes(t *testing.T, dir, archivePath string, modTimes map[string]time.Time, gzipModTime time.Time) {
	t.Helper()
	writeFakeReleaseArchiveCustom(t, dir, archivePath, nil, nil, nil, modTimes, gzipModTime, releaseArchiveFiles())
}

func writeFakeReleaseArchiveWithDuplicates(t *testing.T, dir, archivePath string, duplicates []string) {
	t.Helper()
	writeFakeReleaseArchiveCustom(t, dir, archivePath, nil, duplicates, nil, nil, releaseArchiveTimestamp(), releaseArchiveFiles())
}

func writeFakeReleaseArchiveWithOrder(t *testing.T, dir, archivePath string, order []string) {
	t.Helper()
	writeFakeReleaseArchiveCustom(t, dir, archivePath, nil, nil, nil, nil, releaseArchiveTimestamp(), order)
}

func writeFakeReleaseArchiveCustom(t *testing.T, dir, archivePath string, overrides map[string]string, duplicates []string, modes map[string]int64, modTimes map[string]time.Time, gzipModTime time.Time, order []string) {
	t.Helper()
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.Header.ModTime = gzipModTime
	tarWriter := tar.NewWriter(gzipWriter)
	for _, name := range order {
		var content []byte
		if override, ok := overrides[name]; ok {
			content = []byte(override)
		} else {
			content, err = os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
		}
		mode := int64(expectedReleaseArchiveMode(name))
		if overrideMode, ok := modes[name]; ok {
			mode = overrideMode
		}
		modTime := releaseArchiveTimestamp()
		if overrideModTime, ok := modTimes[name]; ok {
			modTime = overrideModTime
		}
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), ModTime: modTime, Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	for name, override := range overrides {
		if releaseArchiveFileSet()[name] {
			continue
		}
		content := []byte(override)
		mode := int64(expectedReleaseArchiveMode(name))
		if overrideMode, ok := modes[name]; ok {
			mode = overrideMode
		}
		modTime := releaseArchiveTimestamp()
		if overrideModTime, ok := modTimes[name]; ok {
			modTime = overrideModTime
		}
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), ModTime: modTime, Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range duplicates {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		mode := int64(expectedReleaseArchiveMode(name))
		if overrideMode, ok := modes[name]; ok {
			mode = overrideMode
		}
		modTime := releaseArchiveTimestamp()
		if overrideModTime, ok := modTimes[name]; ok {
			modTime = overrideModTime
		}
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), ModTime: modTime, Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(archivePath, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFakeReleaseArchiveChecksumForPath(t, archivePath)
}

func writeFakeReleaseArchiveChecksumForPath(t *testing.T, archivePath string) {
	t.Helper()
	writeFakeReleaseArchiveChecksumForPathWithName(t, archivePath, filepath.Base(archivePath), archivePath+".sha256")
}

func writeFakeReleaseArchiveChecksumForPathWithName(t *testing.T, hashPath, archiveName, sidecarPath string) {
	t.Helper()
	sum, err := fileSHA256(hashPath)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := sum + "  " + archiveName + "\n"
	if err := os.WriteFile(sidecarPath, []byte(sidecar), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sidecarPath, 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func replaceFileText(t *testing.T, path, old, replacement string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	next := strings.Replace(string(content), old, replacement, 1)
	if next == string(content) {
		t.Fatalf("did not find %q in %s", old, path)
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode mismatch for %s: got %04o want %04o", filepath.Base(path), got, want)
	}
}

func assertNoReleaseMetadataTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("unexpected release metadata temp file left behind: %s", entry.Name())
		}
	}
}

func replaceChecksumHash(t *testing.T, path, artifact, replacement string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(content), "\n")
	replaced := false
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == artifact {
			lines[i] = replacement + "  " + artifact
			replaced = true
			break
		}
	}
	if !replaced {
		t.Fatalf("did not find checksum for %s in %s", artifact, path)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readReport(t *testing.T, workspace string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(workspace, "reports", "scenario-run-report.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func testTime() time.Time {
	return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
}
