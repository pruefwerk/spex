package spex

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pruefwerk/spex/internal/workspace"
	"gopkg.in/yaml.v3"
)

var (
	Version     = "0.1.0-dev"
	BuildCommit = "unknown"
	BuildDate   = "unknown"
)

const modulePath = "github.com/pruefwerk/spex"

const maxReleaseArchiveSize int64 = 512 << 20
const maxScenarioReportSummarySize int64 = 4 << 20
const maxKUTTLOutputSize int64 = 4 << 20
const maxCleanupOutputSize int64 = 1 << 20
const maxReleaseCommandOutputSize int64 = 1 << 20

func Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usage(stderr)
		return fmt.Errorf("missing command")
	}

	switch args[0] {
	case "help", "--help", "-h":
		if len(args) > 1 {
			return fmt.Errorf("help does not accept positional arguments: %s", strings.Join(args[1:], ", "))
		}
		usage(stdout)
		return nil
	case "version":
		return runVersion(args[1:], stdout)
	case "validate":
		return runValidate(args[1:], stdout)
	case "compile":
		return runCompile(args[1:], stdout)
	case "suite":
		return runSuite(args[1:], stdout, stderr)
	case "init":
		return runInit(args[1:], stdout)
	case "new":
		return runNew(args[1:], stdout)
	case "explain":
		return runExplain(args[1:], stdout)
	case "catalog":
		return runCatalog(args[1:], stdout)
	case "bundle":
		return runBundle(args[1:], stdout)
	case "schema":
		return runSchema(args[1:], stdout)
	case "doctor":
		return runDoctor(args[1:], stdout)
	case "release":
		return runRelease(args[1:], stdout)
	case "run":
		return runWorkspace(args[1:], stdout, stderr)
	case "clean":
		return runClean(args[1:], stdout)
	default:
		usage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

type versionOutput struct {
	Version     string `json:"version"`
	BuildCommit string `json:"buildCommit"`
	BuildDate   string `json:"buildDate"`
	GoVersion   string `json:"goVersion"`
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
}

func runVersion(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "version"); err != nil {
		return err
	}
	out := versionOutput{
		Version:     Version,
		BuildCommit: BuildCommit,
		BuildDate:   BuildDate,
		GoVersion:   runtime.Version(),
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
	}
	switch *format {
	case "text":
		fmt.Fprintln(stdout, out.Version)
	case "json":
		content, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(content))
	default:
		return fmt.Errorf("version --format must be text or json")
	}
	return nil
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `usage: spex <command> [flags]

Commands:
  version   print build/version information
  validate  validate one scenario and target binding
  compile   generate one KUTTL workspace
  run       run a generated KUTTL workspace and write reports
  clean     delete generated runtime resources
  suite     validate, list, plan, compile, run, or explain a scenario suite
  catalog   list, explain, check, or document reusable catalogs
  bundle    list or explain resolved integration bundles
  schema    list or print embedded JSON Schemas
  doctor    run host and suite preflight checks
  release   verify release artifacts
  init      scaffold a scenario repository
  new       add a scenario file to a scenario repository
  explain   explain one scenario or suite expansion
  help      show this help

Examples:
  spex init scenario-repo --dir acceptance-tests
  spex suite validate --suite acceptance-tests/suite.yaml
  spex suite plan --suite acceptance-tests/suite.yaml --format json
  spex bundle explain --suite acceptance-tests/suite.yaml
  spex catalog check --suite acceptance-tests/suite.yaml --format json
  spex schema list --format json`)
}

func rejectPositionalArgs(fs *flag.FlagSet, command string) error {
	if fs.NArg() == 0 {
		return nil
	}
	return fmt.Errorf("%s does not accept positional arguments: %s", command, strings.Join(fs.Args(), ", "))
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	wasSet := false
	fs.Visit(func(flag *flag.Flag) {
		if flag.Name == name {
			wasSet = true
		}
	})
	return wasSet
}

type releaseManifest struct {
	APIVersion  string                    `yaml:"apiVersion"`
	Version     string                    `yaml:"version"`
	BuildCommit string                    `yaml:"buildCommit"`
	BuildDate   string                    `yaml:"buildDate"`
	GOOS        string                    `yaml:"goos"`
	GOARCH      string                    `yaml:"goarch"`
	Artifacts   []releaseManifestArtifact `yaml:"artifacts"`
}

type releaseManifestArtifact struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256"`
}

type releaseProvenance struct {
	APIVersion  string `json:"apiVersion"`
	Kind        string `json:"kind"`
	Version     string `json:"version"`
	BuildCommit string `json:"buildCommit"`
	BuildDate   string `json:"buildDate"`
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	GoVersion   string `json:"goVersion"`
	ModulePath  string `json:"modulePath"`
}

type releaseDependencyInventory struct {
	APIVersion string                    `json:"apiVersion"`
	Kind       string                    `json:"kind"`
	ModulePath string                    `json:"modulePath"`
	Modules    []releaseDependencyModule `json:"modules"`
}

type releaseDependencyModule struct {
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
	Main    bool   `json:"main,omitempty"`
	Raw     string `json:"raw"`
}

func runRelease(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("release requires subcommand: verify")
	}
	switch args[0] {
	case "archive":
		return runReleaseArchive(args[1:], stdout)
	case "checksum":
		return runReleaseChecksum(args[1:], stdout)
	case "manifest":
		return runReleaseManifest(args[1:], stdout)
	case "module-inventory":
		return runReleaseModuleInventory(args[1:], stdout)
	case "provenance":
		return runReleaseProvenance(args[1:], stdout)
	case "verify":
		return runReleaseVerify(args[1:], stdout)
	default:
		return fmt.Errorf("unknown release subcommand %q", args[0])
	}
}

func runReleaseArchive(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("release archive", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	distDir := fs.String("dist", "dist", "release artifact directory")
	name := fs.String("name", "", "archive file name")
	force := fs.Bool("force", false, "overwrite an existing archive")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "release archive"); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("release archive requires --name")
	}
	if filepath.Base(*name) != *name || strings.Contains(*name, "\\") {
		return fmt.Errorf("release archive --name must be a file name")
	}
	manifest, err := loadReleaseManifest(*distDir)
	if err != nil {
		return err
	}
	expectedName := releaseArchiveName(manifest)
	if *name != expectedName {
		return fmt.Errorf("release archive --name must be %s", expectedName)
	}
	archivePath := filepath.Join(*distDir, *name)
	if err := writeReleaseArchive(*distDir, archivePath, *force); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "release archive written: %s\n", archivePath)
	return nil
}

func runReleaseChecksum(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("release checksum", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	distDir := fs.String("dist", "dist", "release artifact directory")
	archivePath := fs.String("archive", "", "optional release archive path for sidecar checksum")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "release checksum"); err != nil {
		return err
	}
	if *archivePath != "" {
		if flagWasSet(fs, "dist") {
			return fmt.Errorf("release checksum --archive cannot be combined with --dist")
		}
		manifest, err := loadReleaseManifest(filepath.Dir(*archivePath))
		if err != nil {
			return err
		}
		if err := verifyReleaseArchiveName(*archivePath, manifest); err != nil {
			return err
		}
		if err := verifyReleaseArchiveFileMode(*archivePath); err != nil {
			return err
		}
		sum, err := fileSHA256(*archivePath)
		if err != nil {
			return err
		}
		sidecarPath := *archivePath + ".sha256"
		content := fmt.Sprintf("%s  %s\n", sum, filepath.Base(*archivePath))
		if err := writeReleaseMetadataFile(sidecarPath, []byte(content)); err != nil {
			return fmt.Errorf("archive checksum: %w", err)
		}
		fmt.Fprintf(stdout, "archive checksum written: %s\n", sidecarPath)
		return nil
	}
	var b strings.Builder
	for _, name := range releaseArtifacts() {
		if err := verifyReleaseArtifactMode(*distDir, name); err != nil {
			return err
		}
		sum, err := fileSHA256(filepath.Join(*distDir, name))
		if err != nil {
			return err
		}
		b.WriteString(sum)
		b.WriteString("  ")
		b.WriteString(name)
		b.WriteByte('\n')
	}
	path := filepath.Join(*distDir, "SHA256SUMS")
	if err := writeReleaseMetadataFile(path, []byte(b.String())); err != nil {
		return fmt.Errorf("SHA256SUMS: %w", err)
	}
	fmt.Fprintf(stdout, "checksums written: %s\n", path)
	return nil
}

func runReleaseManifest(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("release manifest", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	distDir := fs.String("dist", "dist", "release artifact directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "release manifest"); err != nil {
		return err
	}
	manifest, err := buildReleaseManifest(*distDir)
	if err != nil {
		return err
	}
	content, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := writeReleaseMetadataFile(filepath.Join(*distDir, "release-manifest.yaml"), content); err != nil {
		return fmt.Errorf("release manifest: %w", err)
	}
	fmt.Fprintf(stdout, "release manifest written: %s\n", filepath.Join(*distDir, "release-manifest.yaml"))
	return nil
}

func runReleaseModuleInventory(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("release module-inventory", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	distDir := fs.String("dist", "dist", "release artifact directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "release module-inventory"); err != nil {
		return err
	}
	modulesContent, err := readRegularReleaseFile(filepath.Join(*distDir, "go-modules.txt"), "go-modules.txt")
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(modulesContent)), "\n")
	modules, err := parseReleaseModuleLines(lines)
	if err != nil {
		return err
	}
	inventory := releaseDependencyInventory{
		APIVersion: "spex.dependencies.v0.1",
		Kind:       "GoModuleInventory",
		ModulePath: modulePath,
		Modules:    modules,
	}
	content, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	path := filepath.Join(*distDir, "dependency-inventory.json")
	if err := writeReleaseMetadataFile(path, content); err != nil {
		return fmt.Errorf("dependency-inventory.json: %w", err)
	}
	fmt.Fprintf(stdout, "dependency inventory written: %s\n", path)
	return nil
}

func runReleaseProvenance(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("release provenance", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	distDir := fs.String("dist", "dist", "release artifact directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "release provenance"); err != nil {
		return err
	}
	provenance := releaseProvenance{
		APIVersion:  "spex.provenance.v0.1",
		Kind:        "ReleaseProvenance",
		Version:     Version,
		BuildCommit: BuildCommit,
		BuildDate:   BuildDate,
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		GoVersion:   runtime.Version(),
		ModulePath:  modulePath,
	}
	if err := validateReleaseMetadata(provenance.Version, provenance.BuildCommit, provenance.BuildDate, provenance.GOOS, provenance.GOARCH); err != nil {
		return fmt.Errorf("release provenance: %w", err)
	}
	content, err := json.MarshalIndent(provenance, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	path := filepath.Join(*distDir, "release-provenance.json")
	if err := writeReleaseMetadataFile(path, content); err != nil {
		return fmt.Errorf("release provenance: %w", err)
	}
	fmt.Fprintf(stdout, "release provenance written: %s\n", path)
	return nil
}

func runReleaseVerify(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("release verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	distDir := fs.String("dist", "dist", "release artifact directory")
	archivePath := fs.String("archive", "", "optional release archive to verify")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "release verify"); err != nil {
		return err
	}
	manifest, err := loadReleaseManifest(*distDir)
	if err != nil {
		return err
	}
	if err := verifyReleaseManifest(*distDir, manifest, *archivePath); err != nil {
		return err
	}
	if *archivePath != "" {
		if err := verifyReleaseArchive(*distDir, *archivePath, manifest); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "release verified: %s\n", *distDir)
	return nil
}

func buildReleaseManifest(distDir string) (releaseManifest, error) {
	manifest := releaseManifest{
		APIVersion:  "spex.release.v0.1",
		Version:     Version,
		BuildCommit: BuildCommit,
		BuildDate:   BuildDate,
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
	}
	if err := validateReleaseMetadata(manifest.Version, manifest.BuildCommit, manifest.BuildDate, manifest.GOOS, manifest.GOARCH); err != nil {
		return releaseManifest{}, fmt.Errorf("release manifest: %w", err)
	}
	for _, name := range releaseArtifacts() {
		if err := verifyReleaseArtifactMode(distDir, name); err != nil {
			return releaseManifest{}, err
		}
		sum, err := fileSHA256(filepath.Join(distDir, name))
		if err != nil {
			return releaseManifest{}, err
		}
		manifest.Artifacts = append(manifest.Artifacts, releaseManifestArtifact{Path: name, SHA256: sum})
	}
	return manifest, nil
}

func writeReleaseArchive(distDir, archivePath string, force bool) error {
	dir := filepath.Dir(archivePath)
	base := filepath.Base(archivePath)
	file, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("release archive: %w", err)
	}
	tmpPath := file.Name()
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.Header.ModTime = releaseArchiveTimestamp()
	tarWriter := tar.NewWriter(gzipWriter)
	for _, name := range releaseArchiveFiles() {
		path := filepath.Join(distDir, name)
		if err := verifyReleaseArtifactMode(distDir, name); err != nil {
			_ = file.Close()
			return fmt.Errorf("release archive: %w", err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			_ = file.Close()
			return fmt.Errorf("release archive: %w", err)
		}
		mode := int64(expectedReleaseArchiveMode(name))
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), ModTime: releaseArchiveTimestamp(), Format: tar.FormatUSTAR}); err != nil {
			_ = file.Close()
			return fmt.Errorf("release archive: %w", err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			_ = file.Close()
			return fmt.Errorf("release archive: %w", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = file.Close()
		return fmt.Errorf("release archive: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		_ = file.Close()
		return fmt.Errorf("release archive: %w", err)
	}
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return fmt.Errorf("release archive: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("release archive: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("release archive: %w", err)
	}
	if err := installReleaseArchiveFile(tmpPath, archivePath, force); err != nil {
		return fmt.Errorf("release archive: %w", err)
	}
	if !force {
		if err := os.Remove(tmpPath); err != nil {
			return fmt.Errorf("release archive: %w", err)
		}
	}
	keepTemp = true
	if err := syncReleaseMetadataDir(dir); err != nil {
		return fmt.Errorf("release archive: %w", err)
	}
	return nil
}

func installReleaseArchiveFile(tmpPath, archivePath string, force bool) error {
	if force {
		return os.Rename(tmpPath, archivePath)
	}
	return os.Link(tmpPath, archivePath)
}

func loadReleaseManifest(distDir string) (releaseManifest, error) {
	content, err := readRegularReleaseFile(filepath.Join(distDir, "release-manifest.yaml"), "release manifest")
	if err != nil {
		return releaseManifest{}, err
	}
	var manifest releaseManifest
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return releaseManifest{}, fmt.Errorf("release manifest: %w", err)
	}
	if err := ensureYAMLEOF(decoder); err != nil {
		return releaseManifest{}, fmt.Errorf("release manifest: %w", err)
	}
	if manifest.APIVersion != "spex.release.v0.1" {
		return releaseManifest{}, fmt.Errorf("release manifest: unsupported apiVersion %q", manifest.APIVersion)
	}
	if err := validateReleaseMetadata(manifest.Version, manifest.BuildCommit, manifest.BuildDate, manifest.GOOS, manifest.GOARCH); err != nil {
		return releaseManifest{}, fmt.Errorf("release manifest: %w", err)
	}
	if len(manifest.Artifacts) == 0 {
		return releaseManifest{}, fmt.Errorf("release manifest: artifacts are required")
	}
	return manifest, nil
}

func ensureYAMLEOF(decoder *yaml.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("unexpected trailing YAML document")
}

func validateReleaseMetadata(version, buildCommit, buildDate, goos, goarch string) error {
	if version == "" || buildCommit == "" || buildDate == "" || goos == "" || goarch == "" {
		return fmt.Errorf("version, buildCommit, buildDate, goos, and goarch are required")
	}
	if _, err := time.Parse(time.RFC3339, buildDate); err != nil {
		return fmt.Errorf("buildDate must be RFC3339: %w", err)
	}
	if !isSafeReleaseNameComponent(version) {
		return fmt.Errorf("version %q is not safe for artifact names", version)
	}
	if !isSafeReleaseMetadataValue(buildCommit) {
		return fmt.Errorf("buildCommit %q is not safe for release metadata", buildCommit)
	}
	if !isSafeReleaseNameComponent(goos) {
		return fmt.Errorf("goos %q is not safe for artifact names", goos)
	}
	if !isSafeReleaseNameComponent(goarch) {
		return fmt.Errorf("goarch %q is not safe for artifact names", goarch)
	}
	return nil
}

func isSafeReleaseNameComponent(value string) bool {
	if value == "." || value == ".." {
		return false
	}
	return isSafeReleaseMetadataValue(value)
}

func isSafeReleaseMetadataValue(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r <= 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func verifyReleaseManifest(distDir string, manifest releaseManifest, archivePath string) error {
	checksums, err := loadReleaseChecksums(filepath.Join(distDir, "SHA256SUMS"))
	if err != nil {
		return err
	}
	if err := verifyReleaseDistFileSet(distDir, archivePath); err != nil {
		return err
	}
	expectedArtifacts := releaseArtifactSet()
	expectedArtifactOrder := releaseArtifacts()
	seen := map[string]bool{}
	for i, artifact := range manifest.Artifacts {
		if artifact.Path == "" || artifact.SHA256 == "" {
			return fmt.Errorf("release manifest: artifact path and sha256 are required")
		}
		if !isSHA256Hex(artifact.SHA256) {
			return fmt.Errorf("release manifest: invalid sha256 for %s", artifact.Path)
		}
		if !isSafeReleaseFileName(artifact.Path) {
			return fmt.Errorf("release manifest: artifact path %q must be a file name", artifact.Path)
		}
		if !expectedArtifacts[artifact.Path] {
			return fmt.Errorf("release manifest: unexpected artifact %s", artifact.Path)
		}
		if i >= len(expectedArtifactOrder) || artifact.Path != expectedArtifactOrder[i] {
			want := "<none>"
			if i < len(expectedArtifactOrder) {
				want = expectedArtifactOrder[i]
			}
			return fmt.Errorf("release manifest: artifact order mismatch at index %d: got %s want %s", i, artifact.Path, want)
		}
		if seen[artifact.Path] {
			return fmt.Errorf("release manifest: duplicate artifact %s", artifact.Path)
		}
		actual, err := fileSHA256(filepath.Join(distDir, artifact.Path))
		if err != nil {
			return err
		}
		if actual != artifact.SHA256 {
			return fmt.Errorf("release manifest: sha256 mismatch for %s", artifact.Path)
		}
		if checksums[artifact.Path] != artifact.SHA256 {
			return fmt.Errorf("SHA256SUMS: checksum mismatch for %s", artifact.Path)
		}
		if err := verifyReleaseArtifactMode(distDir, artifact.Path); err != nil {
			return err
		}
		seen[artifact.Path] = true
	}
	for _, required := range releaseArtifacts() {
		if !seen[required] {
			return fmt.Errorf("release manifest: missing artifact %s", required)
		}
	}
	if len(manifest.Artifacts) != len(expectedArtifacts) {
		return fmt.Errorf("release manifest: expected %d artifacts, got %d", len(expectedArtifacts), len(manifest.Artifacts))
	}
	if err := verifyReleaseChecksumOrder(filepath.Join(distDir, "SHA256SUMS"), releaseArtifacts(), "SHA256SUMS"); err != nil {
		return err
	}
	for path := range checksums {
		if !expectedArtifacts[path] {
			return fmt.Errorf("SHA256SUMS: unexpected artifact %s", path)
		}
	}
	if len(checksums) != len(expectedArtifacts) {
		return fmt.Errorf("SHA256SUMS: expected %d artifacts, got %d", len(expectedArtifacts), len(checksums))
	}
	for _, name := range releaseArchiveFiles() {
		if err := verifyReleaseArtifactMode(distDir, name); err != nil {
			return err
		}
	}
	version, err := loadReleaseVersionJSON(filepath.Join(distDir, "version.json"))
	if err != nil {
		return err
	}
	if err := compareReleaseVersion("version.json", manifest, version); err != nil {
		return err
	}
	binaryVersion, err := readBinaryVersion(filepath.Join(distDir, "spex"), "spex version")
	if err != nil {
		return err
	}
	if err := compareReleaseVersion("spex version", manifest, binaryVersion); err != nil {
		return err
	}
	for _, name := range []string{"spex-probe", "spex-probe-influxdb", "spex-probe-redis", "spex-demo-stack"} {
		source := name + " version"
		componentVersion, err := readBinaryVersion(filepath.Join(distDir, name), source)
		if err != nil {
			return err
		}
		if err := compareReleaseVersion(source, manifest, componentVersion); err != nil {
			return err
		}
		if componentVersion.GoVersion != binaryVersion.GoVersion {
			return fmt.Errorf("%s: goVersion %q does not match spex version %q", source, componentVersion.GoVersion, binaryVersion.GoVersion)
		}
	}
	provenance, err := loadReleaseProvenance(filepath.Join(distDir, "release-provenance.json"))
	if err != nil {
		return err
	}
	if err := compareReleaseProvenance(manifest, version, binaryVersion, provenance); err != nil {
		return err
	}
	if err := verifyReleaseTextArtifactsDoNotLeakPaths(distDir); err != nil {
		return err
	}
	return verifyReleaseInventory(distDir)
}

func verifyReleaseDistFileSet(distDir, archivePath string) error {
	allowed := map[string]bool{}
	for _, name := range releaseArchiveFiles() {
		allowed[name] = true
	}
	if archivePath != "" && sameCleanAbsPath(filepath.Dir(archivePath), distDir) {
		archiveName := filepath.Base(archivePath)
		allowed[archiveName] = true
		allowed[archiveName+".sha256"] = true
	}
	entries, err := os.ReadDir(distDir)
	if err != nil {
		return fmt.Errorf("release dist: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !allowed[name] {
			return fmt.Errorf("release dist: unexpected file %s", name)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("release dist: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("release dist: unexpected non-file entry %s", name)
		}
	}
	return nil
}

func sameCleanAbsPath(left, right string) bool {
	leftAbs, err := filepath.Abs(filepath.Clean(left))
	if err != nil {
		return false
	}
	rightAbs, err := filepath.Abs(filepath.Clean(right))
	if err != nil {
		return false
	}
	return leftAbs == rightAbs
}

func absolutePathOrOriginal(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func verifyReleaseArtifactMode(distDir, name string) error {
	info, err := os.Lstat(filepath.Join(distDir, name))
	if err != nil {
		return fmt.Errorf("artifact %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact %s: not a regular file", name)
	}
	wantExecutable := expectedReleaseArchiveMode(name)&0o111 != 0
	gotExecutable := info.Mode()&0o111 != 0
	if wantExecutable && !gotExecutable {
		return fmt.Errorf("artifact %s: must be executable", name)
	}
	if !wantExecutable && gotExecutable {
		return fmt.Errorf("artifact %s: must not be executable", name)
	}
	if gotMode := info.Mode().Perm(); gotMode != expectedReleaseArchiveMode(name) {
		return fmt.Errorf("artifact %s: mode mismatch: got %04o want %04o", name, gotMode, expectedReleaseArchiveMode(name))
	}
	return nil
}

func verifyReleaseTextArtifactsDoNotLeakPaths(distDir string) error {
	for _, name := range releaseTextArtifacts() {
		content, err := readRegularReleaseFile(filepath.Join(distDir, name), name)
		if err != nil {
			return err
		}
		if leaked := firstLocalPathLeak(string(content)); leaked != "" {
			return fmt.Errorf("%s: contains local path leak %q", name, leaked)
		}
	}
	return nil
}

func releaseTextArtifacts() []string {
	return []string{"LICENSE", "COMMERCIAL.md", "CONTRIBUTING.md", "THIRD-PARTY-NOTICES.md", "go-modules.txt", "dependency-inventory.json", "buildinfo.txt", "third-party-licenses.txt", "release-provenance.json", "SHA256SUMS", "version.json", "release-manifest.yaml"}
}

func firstLocalPathLeak(content string) string {
	for _, marker := range []string{"/Users/", "/private/tmp/", "/tmp/", "/var/folders/", "/home/"} {
		if strings.Contains(content, marker) {
			return marker
		}
	}
	for i := 0; i+2 < len(content); i++ {
		if ((content[i] >= 'A' && content[i] <= 'Z') || (content[i] >= 'a' && content[i] <= 'z')) && content[i+1] == ':' && (content[i+2] == '\\' || content[i+2] == '/') {
			if i > 0 && isASCIIAlphaNumeric(content[i-1]) {
				continue
			}
			return content[i : i+3]
		}
	}
	return ""
}

func isASCIIAlphaNumeric(value byte) bool {
	return (value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9')
}

func verifyReleaseInventory(distDir string) error {
	modulesContent, err := readRegularReleaseFile(filepath.Join(distDir, "go-modules.txt"), "go-modules.txt")
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(modulesContent)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return fmt.Errorf("go-modules.txt: dependency inventory is empty")
	}
	firstFields := strings.Fields(lines[0])
	if len(firstFields) == 0 || firstFields[0] != modulePath {
		return fmt.Errorf("go-modules.txt: first module must be %s", modulePath)
	}
	seen := map[string]bool{}
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return fmt.Errorf("go-modules.txt: empty module line %d", i+1)
		}
		if seen[fields[0]] {
			return fmt.Errorf("go-modules.txt: duplicate module %s", fields[0])
		}
		seen[fields[0]] = true
	}
	modules, err := parseReleaseModuleLines(lines)
	if err != nil {
		return err
	}
	if err := verifyReleaseDependencyInventory(distDir, modules); err != nil {
		return err
	}

	buildInfoContent, err := readRegularReleaseFile(filepath.Join(distDir, "buildinfo.txt"), "buildinfo.txt")
	if err != nil {
		return err
	}
	buildInfo := string(buildInfoContent)
	if strings.TrimSpace(buildInfo) == "" {
		return fmt.Errorf("buildinfo.txt: build inventory is empty")
	}
	buildInfoLines := strings.Split(strings.TrimSpace(buildInfo), "\n")
	if len(buildInfoLines) == 0 || strings.TrimSpace(buildInfoLines[0]) != "spex" {
		return fmt.Errorf("buildinfo.txt: first line must be spex")
	}
	if filepath.Base(strings.TrimSpace(buildInfoLines[0])) != strings.TrimSpace(buildInfoLines[0]) {
		return fmt.Errorf("buildinfo.txt: first line must not contain a path")
	}
	if !strings.Contains(buildInfo, "path\t"+modulePath) {
		return fmt.Errorf("buildinfo.txt: missing binary path %s", modulePath)
	}
	if !strings.Contains(buildInfo, "mod\t"+modulePath) {
		return fmt.Errorf("buildinfo.txt: missing module %s", modulePath)
	}
	return verifyThirdPartyLicenses(distDir, lines)
}

func parseReleaseModuleLines(lines []string) ([]releaseDependencyModule, error) {
	if len(lines) == 0 {
		return nil, fmt.Errorf("go-modules.txt: dependency inventory is empty")
	}
	modules := make([]releaseDependencyModule, 0, len(lines))
	seen := map[string]bool{}
	for i, line := range lines {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return nil, fmt.Errorf("go-modules.txt: empty module line %d", i+1)
		}
		path := fields[0]
		if seen[path] {
			return nil, fmt.Errorf("go-modules.txt: duplicate module %s", path)
		}
		seen[path] = true
		module := releaseDependencyModule{Path: path, Raw: line}
		if i == 0 && path == modulePath {
			module.Main = true
		}
		if len(fields) > 1 && fields[1] != "=>" {
			module.Version = fields[1]
		}
		modules = append(modules, module)
	}
	if modules[0].Path != modulePath {
		return nil, fmt.Errorf("go-modules.txt: first module must be %s", modulePath)
	}
	return modules, nil
}

func verifyReleaseDependencyInventory(distDir string, expected []releaseDependencyModule) error {
	content, err := readRegularReleaseFile(filepath.Join(distDir, "dependency-inventory.json"), "dependency-inventory.json")
	if err != nil {
		return err
	}
	var inventory releaseDependencyInventory
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return fmt.Errorf("dependency-inventory.json: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("dependency-inventory.json: %w", err)
	}
	if inventory.APIVersion != "spex.dependencies.v0.1" {
		return fmt.Errorf("dependency-inventory.json: unsupported apiVersion %q", inventory.APIVersion)
	}
	if inventory.Kind != "GoModuleInventory" {
		return fmt.Errorf("dependency-inventory.json: kind must be GoModuleInventory")
	}
	if inventory.ModulePath != modulePath {
		return fmt.Errorf("dependency-inventory.json: modulePath %q does not match %q", inventory.ModulePath, modulePath)
	}
	if len(inventory.Modules) != len(expected) {
		return fmt.Errorf("dependency-inventory.json: expected %d module(s), got %d", len(expected), len(inventory.Modules))
	}
	for i, got := range inventory.Modules {
		want := expected[i]
		if got.Path != want.Path || got.Version != want.Version || got.Main != want.Main || got.Raw != want.Raw {
			return fmt.Errorf("dependency-inventory.json: module %d mismatch: got %#v want %#v", i, got, want)
		}
	}
	return nil
}

func verifyThirdPartyLicenses(distDir string, moduleLines []string) error {
	content, err := readRegularReleaseFile(filepath.Join(distDir, "third-party-licenses.txt"), "third-party-licenses.txt")
	if err != nil {
		return err
	}
	licenses := string(content)
	if !strings.HasPrefix(strings.TrimSpace(licenses), "# third-party licenses") {
		return fmt.Errorf("third-party-licenses.txt: missing header")
	}
	for _, line := range moduleLines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		entry := "module " + fields[0] + " " + fields[1]
		if !strings.Contains(licenses, entry) {
			return fmt.Errorf("third-party-licenses.txt: missing module %s %s", fields[0], fields[1])
		}
	}
	return nil
}

func loadReleaseChecksums(path string) (map[string]string, error) {
	content, err := readRegularReleaseFile(path, "SHA256SUMS")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("SHA256SUMS: invalid line %q", line)
		}
		if !isSafeReleaseFileName(fields[1]) {
			return nil, fmt.Errorf("SHA256SUMS: artifact path %q must be a file name", fields[1])
		}
		if !isSHA256Hex(fields[0]) {
			return nil, fmt.Errorf("SHA256SUMS: invalid sha256 for %s", fields[1])
		}
		if _, ok := out[fields[1]]; ok {
			return nil, fmt.Errorf("SHA256SUMS: duplicate artifact %s", fields[1])
		}
		out[fields[1]] = fields[0]
	}
	return out, nil
}

func verifyReleaseChecksumOrder(path string, expected []string, source string) error {
	content, err := readRegularReleaseFile(path, source)
	if err != nil {
		return err
	}
	index := 0
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return fmt.Errorf("%s: invalid line %q", source, line)
		}
		if !isSafeReleaseFileName(fields[1]) {
			return fmt.Errorf("%s: artifact path %q must be a file name", source, fields[1])
		}
		want := "<none>"
		if index < len(expected) {
			want = expected[index]
		}
		if index >= len(expected) || fields[1] != want {
			return fmt.Errorf("%s: artifact order mismatch at index %d: got %s want %s", source, index, fields[1], want)
		}
		index++
	}
	if index != len(expected) {
		return fmt.Errorf("%s: expected %d artifacts, got %d", source, len(expected), index)
	}
	return nil
}

func isSafeReleaseFileName(name string) bool {
	return name != "" && filepath.Base(name) == name && name != "." && name != ".." && !strings.Contains(name, "\\")
}

func writeReleaseMetadataFile(path string, content []byte) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	keepTemp = true
	return syncReleaseMetadataDir(dir)
}

func syncReleaseMetadataDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}

func readRegularReleaseFile(path, source string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", source)
	}
	maxSize := maxReleaseTextFileSize(filepath.Base(path), source)
	if info.Size() > maxSize {
		return nil, fmt.Errorf("%s: file is too large: got %d bytes, max %d bytes", source, info.Size(), maxSize)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return content, nil
}

func maxReleaseTextFileSize(name, source string) int64 {
	switch name {
	case "go-modules.txt", "dependency-inventory.json", "buildinfo.txt", "third-party-licenses.txt":
		return 16 << 20
	case "SHA256SUMS":
		return 1 << 20
	case "release-manifest.yaml", "version.json", "release-provenance.json":
		return 1 << 20
	default:
		if source == "archive checksum" {
			return 1 << 20
		}
		return 1 << 20
	}
}

func fileSHA256(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("artifact %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("artifact %s: not a regular file", filepath.Base(path))
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("artifact %s: %w", filepath.Base(path), err)
	}
	sum := sha256.Sum256(content)
	return fmt.Sprintf("%x", sum[:]), nil
}

func loadReleaseVersionJSON(path string) (versionOutput, error) {
	content, err := readRegularReleaseFile(path, "version.json")
	if err != nil {
		return versionOutput{}, err
	}
	out, err := decodeVersionOutputJSON(content)
	if err != nil {
		return versionOutput{}, fmt.Errorf("version.json: %w", err)
	}
	return out, nil
}

func loadReleaseProvenance(path string) (releaseProvenance, error) {
	content, err := readRegularReleaseFile(path, "release-provenance.json")
	if err != nil {
		return releaseProvenance{}, err
	}
	var out releaseProvenance
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return releaseProvenance{}, fmt.Errorf("release-provenance.json: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return releaseProvenance{}, fmt.Errorf("release-provenance.json: %w", err)
	}
	if err := validateReleaseProvenance(out); err != nil {
		return releaseProvenance{}, fmt.Errorf("release-provenance.json: %w", err)
	}
	return out, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("unexpected trailing JSON value")
}

func readBinaryVersion(path, source string) (versionOutput, error) {
	output, err := runBoundedCommand(maxReleaseCommandOutputSize, path, "version", "--format", "json")
	if err != nil {
		return versionOutput{}, fmt.Errorf("%s: %w: %s", source, err, strings.TrimSpace(string(output)))
	}
	out, err := decodeVersionOutputJSON(output)
	if err != nil {
		return versionOutput{}, fmt.Errorf("%s: %w", source, err)
	}
	return out, nil
}

func decodeVersionOutputJSON(content []byte) (versionOutput, error) {
	var out versionOutput
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return versionOutput{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return versionOutput{}, err
	}
	if err := validateVersionOutput(out); err != nil {
		return versionOutput{}, err
	}
	return out, nil
}

func validateVersionOutput(out versionOutput) error {
	if out.Version == "" || out.BuildCommit == "" || out.BuildDate == "" || out.GoVersion == "" || out.GOOS == "" || out.GOARCH == "" {
		return fmt.Errorf("version, buildCommit, buildDate, goVersion, goos, and goarch are required")
	}
	if err := validateReleaseMetadata(out.Version, out.BuildCommit, out.BuildDate, out.GOOS, out.GOARCH); err != nil {
		return err
	}
	if !strings.HasPrefix(out.GoVersion, "go") {
		return fmt.Errorf("goVersion %q is not valid", out.GoVersion)
	}
	return nil
}

func validateReleaseProvenance(out releaseProvenance) error {
	if out.APIVersion == "" || out.Kind == "" || out.Version == "" || out.BuildCommit == "" || out.BuildDate == "" || out.GOOS == "" || out.GOARCH == "" || out.GoVersion == "" || out.ModulePath == "" {
		return fmt.Errorf("apiVersion, kind, version, buildCommit, buildDate, goos, goarch, goVersion, and modulePath are required")
	}
	if out.APIVersion != "spex.provenance.v0.1" {
		return fmt.Errorf("unsupported apiVersion %q", out.APIVersion)
	}
	if out.Kind != "ReleaseProvenance" {
		return fmt.Errorf("kind must be ReleaseProvenance")
	}
	if err := validateReleaseMetadata(out.Version, out.BuildCommit, out.BuildDate, out.GOOS, out.GOARCH); err != nil {
		return err
	}
	if !strings.HasPrefix(out.GoVersion, "go") {
		return fmt.Errorf("goVersion %q is not valid", out.GoVersion)
	}
	if out.ModulePath != modulePath {
		return fmt.Errorf("modulePath %q does not match %q", out.ModulePath, modulePath)
	}
	return nil
}

func compareReleaseVersion(source string, manifest releaseManifest, version versionOutput) error {
	if version.Version != manifest.Version {
		return fmt.Errorf("%s: version %q does not match manifest %q", source, version.Version, manifest.Version)
	}
	if version.BuildCommit != manifest.BuildCommit {
		return fmt.Errorf("%s: buildCommit %q does not match manifest %q", source, version.BuildCommit, manifest.BuildCommit)
	}
	if version.BuildDate != manifest.BuildDate {
		return fmt.Errorf("%s: buildDate %q does not match manifest %q", source, version.BuildDate, manifest.BuildDate)
	}
	if version.GOOS != manifest.GOOS {
		return fmt.Errorf("%s: goos %q does not match manifest %q", source, version.GOOS, manifest.GOOS)
	}
	if version.GOARCH != manifest.GOARCH {
		return fmt.Errorf("%s: goarch %q does not match manifest %q", source, version.GOARCH, manifest.GOARCH)
	}
	return nil
}

func compareReleaseProvenance(manifest releaseManifest, version versionOutput, binaryVersion versionOutput, provenance releaseProvenance) error {
	if provenance.APIVersion != "spex.provenance.v0.1" {
		return fmt.Errorf("release-provenance.json: unsupported apiVersion %q", provenance.APIVersion)
	}
	if provenance.Kind != "ReleaseProvenance" {
		return fmt.Errorf("release-provenance.json: kind must be ReleaseProvenance")
	}
	if provenance.Version != manifest.Version {
		return fmt.Errorf("release-provenance.json: version %q does not match manifest %q", provenance.Version, manifest.Version)
	}
	if provenance.BuildCommit != manifest.BuildCommit {
		return fmt.Errorf("release-provenance.json: buildCommit %q does not match manifest %q", provenance.BuildCommit, manifest.BuildCommit)
	}
	if provenance.BuildDate != manifest.BuildDate {
		return fmt.Errorf("release-provenance.json: buildDate %q does not match manifest %q", provenance.BuildDate, manifest.BuildDate)
	}
	if provenance.GOOS != manifest.GOOS {
		return fmt.Errorf("release-provenance.json: goos %q does not match manifest %q", provenance.GOOS, manifest.GOOS)
	}
	if provenance.GOARCH != manifest.GOARCH {
		return fmt.Errorf("release-provenance.json: goarch %q does not match manifest %q", provenance.GOARCH, manifest.GOARCH)
	}
	if provenance.GoVersion == "" {
		return fmt.Errorf("release-provenance.json: goVersion is required")
	}
	if provenance.GoVersion != version.GoVersion {
		return fmt.Errorf("release-provenance.json: goVersion %q does not match version.json %q", provenance.GoVersion, version.GoVersion)
	}
	if provenance.GoVersion != binaryVersion.GoVersion {
		return fmt.Errorf("release-provenance.json: goVersion %q does not match spex version %q", provenance.GoVersion, binaryVersion.GoVersion)
	}
	if provenance.ModulePath != modulePath {
		return fmt.Errorf("release-provenance.json: modulePath %q does not match %q", provenance.ModulePath, modulePath)
	}
	return nil
}

func verifyReleaseArchive(distDir, path string, manifest releaseManifest) error {
	if err := verifyReleaseArchiveName(path, manifest); err != nil {
		return err
	}
	if err := verifyReleaseArchiveFileMode(path); err != nil {
		return err
	}
	actual, err := fileSHA256(path)
	if err != nil {
		return err
	}
	sidecarPath := path + ".sha256"
	if err := verifyReleaseArchiveSidecarMode(sidecarPath); err != nil {
		return err
	}
	if err := verifyReleaseChecksumOrder(sidecarPath, []string{filepath.Base(path)}, "archive checksum"); err != nil {
		return err
	}
	checksums, err := loadReleaseChecksums(sidecarPath)
	if err != nil {
		return fmt.Errorf("archive checksum: %w", err)
	}
	if checksums[filepath.Base(path)] != actual {
		return fmt.Errorf("archive checksum: checksum mismatch for %s", filepath.Base(path))
	}
	if len(checksums) != 1 {
		return fmt.Errorf("archive checksum: expected 1 artifact, got %d", len(checksums))
	}
	expectedSizes, err := releaseArchiveExpectedSizes(distDir)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("archive %s: %w", filepath.Base(path), err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("archive %s: %w", filepath.Base(path), err)
	}
	defer gzipReader.Close()
	if !isNormalizedGzipTimestamp(gzipReader.ModTime) {
		return fmt.Errorf("archive %s: gzip timestamp %s is not normalized", filepath.Base(path), gzipReader.ModTime.UTC().Format(time.RFC3339))
	}
	tarReader := tar.NewReader(gzipReader)
	seen := map[string]bool{}
	archiveHashes := map[string]string{}
	archiveOrderIndex := 0
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("archive %s: %w", filepath.Base(path), err)
		}
		name := header.Name
		if name == "" || filepath.IsAbs(name) || strings.Contains(name, "/") || strings.Contains(name, "\\") || name == "." || name == ".." {
			return fmt.Errorf("archive %s: unsafe path %q", filepath.Base(path), name)
		}
		if seen[name] {
			return fmt.Errorf("archive %s: duplicate file %s", filepath.Base(path), name)
		}
		if _, ok := releaseArchiveFileSet()[name]; !ok {
			return fmt.Errorf("archive %s: unexpected file %s", filepath.Base(path), name)
		}
		want := "<none>"
		if archiveOrderIndex < len(releaseArchiveFiles()) {
			want = releaseArchiveFiles()[archiveOrderIndex]
		}
		if archiveOrderIndex >= len(releaseArchiveFiles()) || name != want {
			return fmt.Errorf("archive %s: file order mismatch at index %d: got %s want %s", filepath.Base(path), archiveOrderIndex, name, want)
		}
		archiveOrderIndex++
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("archive %s: unexpected non-file entry %q", filepath.Base(path), name)
		}
		if header.Size < 0 {
			return fmt.Errorf("archive %s: negative size for %s", filepath.Base(path), name)
		}
		if expectedSize, ok := expectedSizes[name]; ok && header.Size != expectedSize {
			return fmt.Errorf("archive %s: size mismatch for %s: got %d want %d", filepath.Base(path), name, header.Size, expectedSize)
		}
		if !header.ModTime.Equal(releaseArchiveTimestamp()) {
			return fmt.Errorf("archive %s: timestamp mismatch for %s: got %s want %s", filepath.Base(path), name, header.ModTime.UTC().Format(time.RFC3339), releaseArchiveTimestamp().Format(time.RFC3339))
		}
		if expectedArchiveMode, ok := expectedReleaseArchiveModes()[name]; ok {
			if actualMode := header.FileInfo().Mode().Perm(); actualMode != expectedArchiveMode {
				return fmt.Errorf("archive %s: mode mismatch for %s: got %04o want %04o", filepath.Base(path), name, actualMode, expectedArchiveMode)
			}
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, tarReader); err != nil {
			return fmt.Errorf("archive %s: %w", filepath.Base(path), err)
		}
		seen[name] = true
		archiveHashes[name] = fmt.Sprintf("%x", hash.Sum(nil))
	}
	expectedArchiveFiles := releaseArchiveFileSet()
	for name := range seen {
		if !expectedArchiveFiles[name] {
			return fmt.Errorf("archive %s: unexpected file %s", filepath.Base(path), name)
		}
	}
	for _, required := range releaseArchiveFiles() {
		if !seen[required] {
			return fmt.Errorf("archive %s: missing %s", filepath.Base(path), required)
		}
	}
	if len(seen) != len(expectedArchiveFiles) {
		return fmt.Errorf("archive %s: expected %d files, got %d", filepath.Base(path), len(expectedArchiveFiles), len(seen))
	}
	for _, artifact := range manifest.Artifacts {
		if archiveHashes[artifact.Path] != artifact.SHA256 {
			return fmt.Errorf("archive %s: sha256 mismatch for %s", filepath.Base(path), artifact.Path)
		}
	}
	for _, required := range []string{"SHA256SUMS", "version.json", "release-manifest.yaml"} {
		distSHA, err := fileSHA256(filepath.Join(distDir, required))
		if err != nil {
			return err
		}
		if archiveHashes[required] != distSHA {
			return fmt.Errorf("archive %s: %s does not match dist", filepath.Base(path), required)
		}
	}
	return nil
}

func releaseArchiveExpectedSizes(distDir string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, name := range releaseArchiveFiles() {
		info, err := os.Lstat(filepath.Join(distDir, name))
		if err != nil {
			return nil, fmt.Errorf("artifact %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("artifact %s: not a regular file", name)
		}
		out[name] = info.Size()
	}
	return out, nil
}

func verifyReleaseArchiveFileMode(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("archive %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("archive %s: not a regular file", filepath.Base(path))
	}
	if info.Size() > maxReleaseArchiveSize {
		return fmt.Errorf("archive %s: file is too large: got %d bytes, max %d bytes", filepath.Base(path), info.Size(), maxReleaseArchiveSize)
	}
	if info.Mode()&0o111 != 0 {
		return fmt.Errorf("archive %s: must not be executable", filepath.Base(path))
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o644 {
		return fmt.Errorf("archive %s: mode mismatch: got %04o want 0644", filepath.Base(path), gotMode)
	}
	return nil
}

func verifyReleaseArchiveSidecarMode(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("archive checksum: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("archive checksum: %s is not a regular file", filepath.Base(path))
	}
	if info.Mode()&0o111 != 0 {
		return fmt.Errorf("archive checksum: %s must not be executable", filepath.Base(path))
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o644 {
		return fmt.Errorf("archive checksum: %s mode mismatch: got %04o want 0644", filepath.Base(path), gotMode)
	}
	return nil
}

func verifyReleaseArchiveName(path string, manifest releaseManifest) error {
	expected := releaseArchiveName(manifest)
	if filepath.Base(path) != expected {
		return fmt.Errorf("archive %s: expected archive name %s", filepath.Base(path), expected)
	}
	return nil
}

func releaseArchiveName(manifest releaseManifest) string {
	return fmt.Sprintf("spex_%s_%s_%s.tar.gz", manifest.Version, manifest.GOOS, manifest.GOARCH)
}

func releaseArtifacts() []string {
	return []string{"spex", "spex-probe", "spex-probe-influxdb", "spex-probe-redis", "spex-demo-stack", "LICENSE", "COMMERCIAL.md", "CONTRIBUTING.md", "THIRD-PARTY-NOTICES.md", "go-modules.txt", "dependency-inventory.json", "buildinfo.txt", "third-party-licenses.txt", "release-provenance.json"}
}

func releaseArchiveFiles() []string {
	return []string{"spex", "spex-probe", "spex-probe-influxdb", "spex-probe-redis", "spex-demo-stack", "LICENSE", "COMMERCIAL.md", "CONTRIBUTING.md", "THIRD-PARTY-NOTICES.md", "go-modules.txt", "dependency-inventory.json", "buildinfo.txt", "third-party-licenses.txt", "release-provenance.json", "SHA256SUMS", "version.json", "release-manifest.yaml"}
}

func releaseArtifactSet() map[string]bool {
	out := map[string]bool{}
	for _, name := range releaseArtifacts() {
		out[name] = true
	}
	return out
}

func releaseArchiveFileSet() map[string]bool {
	out := map[string]bool{}
	for _, name := range releaseArchiveFiles() {
		out[name] = true
	}
	return out
}

func releaseArchiveTimestamp() time.Time {
	return time.Unix(0, 0).UTC()
}

func isNormalizedGzipTimestamp(value time.Time) bool {
	return value.IsZero() || value.Equal(releaseArchiveTimestamp())
}

func expectedReleaseArchiveMode(name string) os.FileMode {
	if name == "spex" || name == "spex-probe" || name == "spex-probe-influxdb" || name == "spex-probe-redis" || name == "spex-demo-stack" {
		return 0o755
	}
	return 0o644
}

func expectedReleaseArchiveModes() map[string]os.FileMode {
	out := map[string]os.FileMode{}
	for _, name := range releaseArchiveFiles() {
		out[name] = expectedReleaseArchiveMode(name)
	}
	return out
}

func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return false
	}
	return true
}

func runValidate(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	scenarioPath := fs.String("scenario", "", "scenario YAML path")
	bindingPath := fs.String("binding", "", "target binding YAML path")
	integrationProfilePath := fs.String("integration-profile", "", "optional KUTTL-native integration profile YAML path")
	kubeContext := fs.String("kube-context", "", "override target binding kubeContext")
	namespace := fs.String("namespace", "", "override target binding namespace")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "validate"); err != nil {
		return err
	}
	if *scenarioPath == "" || *bindingPath == "" {
		return fmt.Errorf("validate requires --scenario and --binding")
	}
	inputs, err := workspace.LoadInputs(*scenarioPath, *bindingPath)
	if err != nil {
		return err
	}
	if *integrationProfilePath != "" {
		profile, err := workspace.LoadIntegrationProfile(*integrationProfilePath)
		if err != nil {
			return fmt.Errorf("integration profile: %w", err)
		}
		inputs.Integration = &profile
		inputs.IntegrationProfilePath = absolutePathOrOriginal(*integrationProfilePath)
	}
	if *kubeContext != "" {
		inputs.KubeContext = *kubeContext
		inputs.Binding.Spec.KubeContext = *kubeContext
	}
	if *namespace != "" {
		inputs.Namespace = *namespace
		inputs.Binding.Spec.Namespace = *namespace
	}
	if err := workspace.ValidateRuntimeInputs(inputs); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "validation passed")
	return nil
}

func runCompile(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("compile", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	scenarioPath := fs.String("scenario", "", "scenario YAML path")
	bindingPath := fs.String("binding", "", "target binding YAML path")
	integrationProfilePath := fs.String("integration-profile", "", "optional KUTTL-native integration profile YAML path")
	outPath := fs.String("out", "", "output workspace directory")
	runID := fs.String("run-id", "", "fixed run ID for deterministic output")
	kubeContext := fs.String("kube-context", "", "override target binding kubeContext")
	namespace := fs.String("namespace", "", "override target binding namespace")
	startKIND := fs.Bool("start-kind", false, "emit kuttl-test.yaml with startKIND: true")
	probeImage := fs.String("probe-image", "", "override target binding probe image")
	probeImagePullPolicy := fs.String("probe-image-pull-policy", "", "override target binding probe imagePullPolicy")
	repoRoot := fs.String("repo-root", "", "override ${repoRoot} for integration profile rendering")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "compile"); err != nil {
		return err
	}
	if *scenarioPath == "" || *bindingPath == "" || *outPath == "" {
		return fmt.Errorf("compile requires --scenario, --binding, and --out")
	}
	inputs, err := workspace.LoadInputs(*scenarioPath, *bindingPath)
	if err != nil {
		return err
	}
	if *runID != "" {
		inputs.RunID = *runID
	}
	if *integrationProfilePath != "" {
		profile, err := workspace.LoadIntegrationProfile(*integrationProfilePath)
		if err != nil {
			return fmt.Errorf("integration profile: %w", err)
		}
		inputs.Integration = &profile
		inputs.IntegrationProfilePath = absolutePathOrOriginal(*integrationProfilePath)
	}
	if *kubeContext != "" {
		inputs.KubeContext = *kubeContext
		inputs.Binding.Spec.KubeContext = *kubeContext
	}
	if *namespace != "" {
		inputs.Namespace = *namespace
		inputs.Binding.Spec.Namespace = *namespace
	}
	if *startKIND {
		inputs.StartKIND = true
	}
	if *probeImage != "" {
		inputs.Binding.Spec.Probe.Image = *probeImage
	}
	if *probeImagePullPolicy != "" {
		inputs.Binding.Spec.Probe.ImagePullPolicy = *probeImagePullPolicy
	}
	if *repoRoot != "" {
		inputs.RepoRoot = *repoRoot
	}
	if err := workspace.ValidateRuntimeInputs(inputs); err != nil {
		return err
	}
	if err := validateOutputDir(*outPath); err != nil {
		return err
	}
	if err := os.RemoveAll(*outPath); err != nil {
		return err
	}
	if err := workspace.Generate(*outPath, inputs); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "workspace written: %s\n", *outPath)
	return nil
}

func runSuite(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		suiteUsage(stdout)
		return nil
	}
	switch args[0] {
	case "help", "--help", "-h":
		if len(args) > 1 {
			return fmt.Errorf("suite help does not accept positional arguments: %s", strings.Join(args[1:], ", "))
		}
		suiteUsage(stdout)
		return nil
	case "validate":
		return runSuiteValidate(args[1:], stdout)
	case "list":
		return runSuiteList(args[1:], stdout)
	case "plan":
		return runSuitePlan(args[1:], stdout)
	case "compile":
		return runSuiteCompile(args[1:], stdout)
	case "run":
		return runSuiteRun(args[1:], stdout, stderr)
	case "explain":
		return runSuiteExplain(args[1:], stdout)
	default:
		return fmt.Errorf("unknown suite command %q", args[0])
	}
}

func suiteUsage(stdout io.Writer) {
	fmt.Fprintln(stdout, `usage: spex suite <command> [flags]

Suite commands:
  validate  validate all scenarios in a suite
  list      list suite scenarios
  plan      show binding/profile/catalog resolution and required inputs
  explain   show resolved scenario operations
  compile   generate KUTTL workspaces for all suite scenarios
  run       generate and run all suite scenario workspaces

Examples:
  spex suite validate --suite suite.yaml
  spex suite validate --suite suite.yaml --include-tag smoke --exclude-tag slow
  spex suite plan --suite suite.yaml --format json
  spex suite run --suite suite.yaml --out generated/run`)
}

type suiteFlags struct {
	suitePath            string
	outPath              string
	runID                string
	kubeContext          string
	namespace            string
	probeImage           string
	probeImagePullPolicy string
	repoRoot             string
	command              string
	format               string
	retainRuntime        bool
	collectResources     bool
	failFast             bool
	includeTags          stringListFlag
	excludeTags          stringListFlag
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(strings.TrimPrefix(part, "@"))
		if part == "" {
			continue
		}
		*f = append(*f, part)
	}
	return nil
}

func parseSuiteFlags(command string, args []string) (suiteFlags, error) {
	fs := flag.NewFlagSet("suite "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags suiteFlags
	fs.StringVar(&flags.suitePath, "suite", "", "scenario suite YAML path")
	fs.StringVar(&flags.outPath, "out", "", "output workspace root")
	fs.StringVar(&flags.runID, "run-id", "", "fixed run ID prefix for deterministic output")
	fs.StringVar(&flags.kubeContext, "kube-context", "", "override target binding kubeContext")
	fs.StringVar(&flags.namespace, "namespace", "", "override target binding namespace")
	fs.StringVar(&flags.probeImage, "probe-image", "", "override target binding probe image")
	fs.StringVar(&flags.probeImagePullPolicy, "probe-image-pull-policy", "", "override target binding probe imagePullPolicy")
	fs.StringVar(&flags.repoRoot, "repo-root", "", "override ${repoRoot} for integration profile rendering")
	fs.StringVar(&flags.command, "command", "kubectl", "KUTTL command executable")
	fs.StringVar(&flags.format, "format", "text", "output format for commands that support it")
	fs.BoolVar(&flags.retainRuntime, "retain-runtime-resources", false, "keep generated Jobs and runtime ConfigMaps after evidence collection")
	fs.BoolVar(&flags.collectResources, "collect-resource-usage", false, "collect best-effort kubectl top pod evidence after each scenario run")
	fs.BoolVar(&flags.failFast, "fail-fast", false, "stop after the first scenario run failure")
	fs.Var(&flags.includeTags, "include-tag", "include only scenarios with this tag; may be repeated or comma-separated")
	fs.Var(&flags.excludeTags, "exclude-tag", "exclude scenarios with this tag; may be repeated or comma-separated")
	if err := fs.Parse(args); err != nil {
		return flags, err
	}
	if err := rejectPositionalArgs(fs, "suite "+command); err != nil {
		return flags, err
	}
	if flags.suitePath == "" {
		return flags, fmt.Errorf("suite %s requires --suite", command)
	}
	return flags, nil
}

func runSuiteList(args []string, stdout io.Writer) error {
	flags, format, err := parseSuiteListFlags(args)
	if err != nil {
		return err
	}
	resolved, err := workspace.LoadScenarioSuite(flags.suitePath)
	if err != nil {
		return fmt.Errorf("suite: %w", err)
	}
	inputs, err := loadSuiteInputs(resolved, flags)
	if err != nil {
		return err
	}
	if format == "json" {
		out := suiteListOutput{
			Suite:                  resolved.Suite.Metadata.Name,
			SuiteFile:              resolved.Path,
			BindingFile:            resolved.BindingPath,
			IntegrationProfileFile: resolved.IntegrationProfilePath,
			CatalogFiles:           resolved.CatalogPaths,
		}
		for _, input := range inputs {
			out.Scenarios = append(out.Scenarios, suiteListScenario{
				Name: input.ScenarioName,
				File: input.ScenarioPath,
				Tags: input.Scenario.Metadata.Tags,
			})
		}
		content, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(content))
		return nil
	}
	fmt.Fprintf(stdout, "suite: %s\n", resolved.Suite.Metadata.Name)
	for _, input := range inputs {
		if len(input.Scenario.Metadata.Tags) > 0 {
			fmt.Fprintf(stdout, "- %s [%s] (%s)\n", input.ScenarioName, strings.Join(input.Scenario.Metadata.Tags, ", "), input.ScenarioPath)
			continue
		}
		fmt.Fprintf(stdout, "- %s (%s)\n", input.ScenarioName, input.ScenarioPath)
	}
	return nil
}

type suiteListOutput struct {
	Suite                  string              `json:"suite"`
	SuiteFile              string              `json:"suiteFile"`
	BindingFile            string              `json:"bindingFile"`
	IntegrationProfileFile string              `json:"integrationProfileFile,omitempty"`
	CatalogFiles           []string            `json:"catalogFiles,omitempty"`
	Scenarios              []suiteListScenario `json:"scenarios"`
}

type suiteListScenario struct {
	Name string   `json:"name"`
	File string   `json:"file"`
	Tags []string `json:"tags,omitempty"`
}

func parseSuiteListFlags(args []string) (suiteFlags, string, error) {
	fs := flag.NewFlagSet("suite list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags suiteFlags
	var format string
	fs.StringVar(&flags.suitePath, "suite", "", "scenario suite YAML path")
	fs.StringVar(&flags.kubeContext, "kube-context", "", "override target binding kubeContext")
	fs.StringVar(&flags.namespace, "namespace", "", "override target binding namespace")
	fs.StringVar(&flags.probeImage, "probe-image", "", "override target binding probe image")
	fs.StringVar(&flags.probeImagePullPolicy, "probe-image-pull-policy", "", "override target binding probe imagePullPolicy")
	fs.StringVar(&flags.repoRoot, "repo-root", "", "override ${repoRoot} for integration profile rendering")
	fs.StringVar(&format, "format", "text", "output format: text or json")
	fs.Var(&flags.includeTags, "include-tag", "include only scenarios with this tag; may be repeated or comma-separated")
	fs.Var(&flags.excludeTags, "exclude-tag", "exclude scenarios with this tag; may be repeated or comma-separated")
	if err := fs.Parse(args); err != nil {
		return flags, "", err
	}
	if err := rejectPositionalArgs(fs, "suite list"); err != nil {
		return flags, "", err
	}
	if flags.suitePath == "" {
		return flags, "", fmt.Errorf("suite list requires --suite")
	}
	switch format {
	case "text", "json":
		return flags, format, nil
	default:
		return flags, "", fmt.Errorf("suite list --format must be text or json")
	}
}

func runSuiteValidate(args []string, stdout io.Writer) error {
	flags, err := parseSuiteFlags("validate", args)
	if err != nil {
		return err
	}
	resolved, err := workspace.LoadScenarioSuite(flags.suitePath)
	if err != nil {
		return fmt.Errorf("suite: %w", err)
	}
	inputs, err := loadSuiteInputs(resolved, flags)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "suite validation passed: %d scenario(s)\n", len(inputs))
	return nil
}

func runSuiteCompile(args []string, stdout io.Writer) error {
	flags, err := parseSuiteFlags("compile", args)
	if err != nil {
		return err
	}
	resolved, err := workspace.LoadScenarioSuite(flags.suitePath)
	if err != nil {
		return fmt.Errorf("suite: %w", err)
	}
	inputs, err := loadSuiteInputs(resolved, flags)
	if err != nil {
		return err
	}
	outRoot := suiteOutputRoot(resolved, flags)
	if err := validateOutputDir(outRoot); err != nil {
		return err
	}
	if err := os.RemoveAll(outRoot); err != nil {
		return err
	}
	if _, err := generateSuiteWorkspaces(outRoot, resolved, inputs, stdout); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "suite workspace written: %s\n", outRoot)
	return nil
}

func runSuiteRun(args []string, stdout, stderr io.Writer) error {
	flags, err := parseSuiteFlags("run", args)
	if err != nil {
		return err
	}
	resolved, err := workspace.LoadScenarioSuite(flags.suitePath)
	if err != nil {
		return fmt.Errorf("suite: %w", err)
	}
	inputs, err := loadSuiteInputs(resolved, flags)
	if err != nil {
		return err
	}
	outRoot := suiteOutputRoot(resolved, flags)
	if err := validateOutputDir(outRoot); err != nil {
		return err
	}
	if err := os.RemoveAll(outRoot); err != nil {
		return err
	}
	workspaces, err := generateSuiteWorkspaces(outRoot, resolved, inputs, stdout)
	if err != nil {
		return err
	}
	failFast := flags.failFast || resolved.Suite.Spec.FailFast
	var failed []string
	for _, workspacePath := range workspaces {
		runArgs := []string{"--workspace", workspacePath, "--command", flags.command}
		if flags.retainRuntime {
			runArgs = append(runArgs, "--retain-runtime-resources")
		}
		if flags.collectResources {
			runArgs = append(runArgs, "--collect-resource-usage")
		}
		if err := runWorkspace(runArgs, stdout, stderr); err != nil {
			failed = append(failed, filepath.Base(workspacePath))
			if failFast {
				break
			}
		}
	}
	if len(failed) > 0 {
		if err := writeSuiteReports(resolved, flags, outRoot, workspaces); err != nil {
			return fmt.Errorf("suite failed: %s; report write failed: %w", strings.Join(failed, ", "), err)
		}
		return fmt.Errorf("suite failed: %s", strings.Join(failed, ", "))
	}
	if err := writeSuiteReports(resolved, flags, outRoot, workspaces); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "suite passed: %d scenario(s)\n", len(workspaces))
	return nil
}

type junitTestsuites struct {
	XMLName  xml.Name         `xml:"testsuites"`
	Tests    int              `xml:"tests,attr"`
	Failures int              `xml:"failures,attr"`
	Suites   []junitTestsuite `xml:"testsuite"`
}

type junitTestsuite struct {
	Name      string          `xml:"name,attr"`
	Tests     int             `xml:"tests,attr"`
	Failures  int             `xml:"failures,attr"`
	TestCases []junitTestcase `xml:"testcase"`
}

type junitTestcase struct {
	Name    string        `xml:"name,attr"`
	Class   string        `xml:"classname,attr"`
	Failure *junitFailure `xml:"failure,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

type suiteRunReport struct {
	APIVersion string                `yaml:"apiVersion" json:"apiVersion"`
	Kind       string                `yaml:"kind" json:"kind"`
	Metadata   suiteRunReportMeta    `yaml:"metadata" json:"metadata"`
	Status     suiteRunReportStatus  `yaml:"status" json:"status"`
	Spec       suiteRunReportSpec    `yaml:"spec" json:"spec"`
	Scenarios  []suiteRunScenarioRef `yaml:"scenarios" json:"scenarios"`
}

type suiteRunReportMeta struct {
	Name string `yaml:"name" json:"name"`
}

type suiteRunReportStatus struct {
	Result   string `yaml:"result" json:"result"`
	Tests    int    `yaml:"tests" json:"tests"`
	Failures int    `yaml:"failures" json:"failures"`
}

type suiteRunReportSpec struct {
	SuiteFile     string `yaml:"suiteFile" json:"suiteFile"`
	WorkspaceRoot string `yaml:"workspaceRoot" json:"workspaceRoot"`
	ReportDir     string `yaml:"reportDir" json:"reportDir"`
}

type suiteRunScenarioRef struct {
	Name           string  `yaml:"name" json:"name"`
	Result         string  `yaml:"result" json:"result"`
	Workspace      string  `yaml:"workspace" json:"workspace"`
	Report         string  `yaml:"report" json:"report"`
	FailureMessage *string `yaml:"failureMessage,omitempty" json:"failureMessage,omitempty"`
}

func writeSuiteReports(resolved workspace.ResolvedScenarioSuite, flags suiteFlags, outRoot string, workspaces []string) error {
	reportDir := suiteReportDir(resolved, flags, outRoot)
	if suiteWantsFormat(resolved, "yaml") {
		if err := writeSuiteYAML(resolved, reportDir, outRoot, workspaces); err != nil {
			return err
		}
	}
	if suiteWantsFormat(resolved, "json") {
		if err := writeSuiteJSON(resolved, reportDir, outRoot, workspaces); err != nil {
			return err
		}
	}
	if suiteWantsFormat(resolved, "junit") {
		return writeSuiteJUnit(reportDir, workspaces)
	}
	return nil
}

func suiteWantsFormat(resolved workspace.ResolvedScenarioSuite, wanted string) bool {
	if len(resolved.Suite.Spec.Reports.Format) == 0 {
		return true
	}
	for _, format := range resolved.Suite.Spec.Reports.Format {
		if format == wanted {
			return true
		}
	}
	return false
}

func suiteReportDir(resolved workspace.ResolvedScenarioSuite, flags suiteFlags, outRoot string) string {
	if flags.outPath != "" {
		return filepath.Join(outRoot, "reports")
	}
	if resolved.Suite.Spec.Reports.OutputDir == "" {
		return filepath.Join(outRoot, "reports")
	}
	if filepath.IsAbs(resolved.Suite.Spec.Reports.OutputDir) {
		return resolved.Suite.Spec.Reports.OutputDir
	}
	return filepath.Join(filepath.Dir(resolved.Path), resolved.Suite.Spec.Reports.OutputDir)
}

func writeSuiteYAML(resolved workspace.ResolvedScenarioSuite, reportDir, outRoot string, workspaces []string) error {
	report := buildSuiteReport(resolved, reportDir, outRoot, workspaces)
	content, err := yaml.Marshal(report)
	if err != nil {
		return err
	}
	if err := ensureSafeDirectory(reportDir, 0o755); err != nil {
		return err
	}
	return writeReportFile(filepath.Join(reportDir, "suite-run-report.yaml"), content)
}

func writeSuiteJSON(resolved workspace.ResolvedScenarioSuite, reportDir, outRoot string, workspaces []string) error {
	report := buildSuiteReport(resolved, reportDir, outRoot, workspaces)
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := ensureSafeDirectory(reportDir, 0o755); err != nil {
		return err
	}
	return writeReportFile(filepath.Join(reportDir, "suite-run-report.json"), content)
}

func buildSuiteReport(resolved workspace.ResolvedScenarioSuite, reportDir, outRoot string, workspaces []string) suiteRunReport {
	report := suiteRunReport{
		APIVersion: "spex.suite.report.v0.1",
		Kind:       "SuiteRunReport",
		Metadata: suiteRunReportMeta{
			Name: resolved.Suite.Metadata.Name,
		},
		Status: suiteRunReportStatus{
			Result: "passed",
			Tests:  len(workspaces),
		},
		Spec: suiteRunReportSpec{
			SuiteFile:     resolved.Path,
			WorkspaceRoot: outRoot,
			ReportDir:     reportDir,
		},
	}
	for _, workspacePath := range workspaces {
		scenarioReportPath := filepath.Join(workspacePath, "reports", "scenario-run-report.yaml")
		name, status, message := readReportSummary(scenarioReportPath)
		if name == "" {
			name = filepath.Base(workspacePath)
		}
		scenario := suiteRunScenarioRef{
			Name:      name,
			Result:    status,
			Workspace: workspacePath,
			Report:    scenarioReportPath,
		}
		if status != "passed" {
			report.Status.Failures++
			report.Status.Result = "failed"
			scenario.FailureMessage = &message
		}
		report.Scenarios = append(report.Scenarios, scenario)
	}
	return report
}

func writeSuiteJUnit(reportDir string, workspaces []string) error {
	result := junitTestsuites{}
	suite := junitTestsuite{Name: "spex", Tests: len(workspaces)}
	for _, workspacePath := range workspaces {
		reportPath := filepath.Join(workspacePath, "reports", "scenario-run-report.yaml")
		name, status, message := readReportSummary(reportPath)
		if name == "" {
			name = filepath.Base(workspacePath)
		}
		tc := junitTestcase{Name: name, Class: "spex.scenario"}
		if status != "passed" {
			suite.Failures++
			tc.Failure = &junitFailure{Message: message, Body: message}
		}
		suite.TestCases = append(suite.TestCases, tc)
	}
	result.Tests = suite.Tests
	result.Failures = suite.Failures
	result.Suites = []junitTestsuite{suite}
	content, err := xml.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := ensureSafeDirectory(reportDir, 0o755); err != nil {
		return err
	}
	return writeReportFile(filepath.Join(reportDir, "suite-junit.xml"), append([]byte(xml.Header), content...))
}

func readReportSummary(path string) (name, status, message string) {
	content, err := readRegularScenarioReportSummary(path)
	if err != nil {
		return "", "error", err.Error()
	}
	var report ScenarioRunReport
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	if err := decoder.Decode(&report); err != nil {
		return "", "error", fmt.Sprintf("%s: %v", filepath.Base(path), err)
	}
	if err := ensureYAMLEOF(decoder); err != nil {
		return "", "error", fmt.Sprintf("%s: %v", filepath.Base(path), err)
	}
	if report.APIVersion != "spex.report.v0.1" {
		return "", "error", fmt.Sprintf("%s: unsupported apiVersion %q", filepath.Base(path), report.APIVersion)
	}
	if report.Kind != "ScenarioRunReport" {
		return "", "error", fmt.Sprintf("%s: unsupported kind %q", filepath.Base(path), report.Kind)
	}
	name = report.Metadata.Name
	status = report.Status.Result
	if report.Status.FailureMessage != nil {
		message = *report.Status.FailureMessage
	}
	if status == "" {
		status = "error"
	}
	if message == "" && status != "passed" {
		message = "scenario did not pass"
	}
	return name, status, message
}

func readRegularScenarioReportSummary(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", filepath.Base(path))
	}
	if info.Size() > maxScenarioReportSummarySize {
		return nil, fmt.Errorf("%s: file is too large: got %d bytes, max %d bytes", filepath.Base(path), info.Size(), maxScenarioReportSummarySize)
	}
	return os.ReadFile(path)
}

func runSuiteExplain(args []string, stdout io.Writer) error {
	flags, err := parseSuiteFlags("explain", args)
	if err != nil {
		return err
	}
	if flags.format != "text" && flags.format != "json" {
		return fmt.Errorf("suite explain --format must be text or json")
	}
	resolved, err := workspace.LoadScenarioSuite(flags.suitePath)
	if err != nil {
		return fmt.Errorf("suite: %w", err)
	}
	inputs, err := loadSuiteInputs(resolved, flags)
	if err != nil {
		return err
	}
	if flags.format == "json" {
		content, err := json.MarshalIndent(buildSuiteExplainOutput(resolved, inputs), "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(content))
		return nil
	}
	fmt.Fprintf(stdout, "suite: %s\n", resolved.Suite.Metadata.Name)
	fmt.Fprintf(stdout, "binding: %s\n", resolved.BindingPath)
	if resolved.IntegrationProfilePath != "" {
		fmt.Fprintf(stdout, "integrationProfile: %s\n", resolved.IntegrationProfilePath)
	}
	for _, path := range resolved.CatalogPaths {
		fmt.Fprintf(stdout, "catalog: %s\n", path)
	}
	for _, input := range inputs {
		writeInputsExplanation(stdout, input)
	}
	return nil
}

type suiteExplainOutput struct {
	Suite                  string                 `json:"suite"`
	SuiteFile              string                 `json:"suiteFile"`
	BindingFile            string                 `json:"bindingFile"`
	IntegrationProfileFile string                 `json:"integrationProfileFile,omitempty"`
	CatalogFiles           []string               `json:"catalogFiles,omitempty"`
	Providers              []suiteProvider        `json:"providers,omitempty"`
	Scenarios              []suiteExplainScenario `json:"scenarios"`
}

type suiteExplainScenario struct {
	Name         string                  `json:"name"`
	File         string                  `json:"file"`
	Tags         []string                `json:"tags,omitempty"`
	Binding      string                  `json:"bindingFile"`
	Namespace    string                  `json:"namespace"`
	KubeContext  string                  `json:"kubeContext,omitempty"`
	HelmApps     []suitePlanHelmApp      `json:"helmApps,omitempty"`
	Steps        []suiteExplainStep      `json:"steps,omitempty"`
	Capabilities []suiteProvider         `json:"capabilities,omitempty"`
	Operations   []suiteExplainOperation `json:"operations"`
}

type suiteExplainStep struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type suiteExplainOperation struct {
	ID                 string `json:"id"`
	Type               string `json:"type"`
	Provider           string `json:"provider,omitempty"`
	BindingKind        string `json:"bindingKind,omitempty"`
	BindingName        string `json:"bindingName,omitempty"`
	After              string `json:"after,omitempty"`
	Exchange           string `json:"exchange,omitempty"`
	RoutingKey         string `json:"routingKey,omitempty"`
	Queue              string `json:"queue,omitempty"`
	Topic              string `json:"topic,omitempty"`
	TopicRef           string `json:"topicRef,omitempty"`
	PayloadTemplateRef string `json:"payloadTemplateRef,omitempty"`
	QueryRef           string `json:"queryRef,omitempty"`
	Collection         string `json:"collection,omitempty"`
	ArgCount           int    `json:"argCount,omitempty"`
	CorrelationID      string `json:"correlationId,omitempty"`
	VariableCount      int    `json:"variableCount,omitempty"`
	MatcherCount       int    `json:"matcherCount,omitempty"`
}

func buildSuiteExplainOutput(resolved workspace.ResolvedScenarioSuite, inputs []workspace.Inputs) suiteExplainOutput {
	out := suiteExplainOutput{
		Suite:                  resolved.Suite.Metadata.Name,
		SuiteFile:              resolved.Path,
		BindingFile:            resolved.BindingPath,
		IntegrationProfileFile: resolved.IntegrationProfilePath,
		CatalogFiles:           resolved.CatalogPaths,
	}
	providerSet := map[string]suiteProvider{}
	for _, input := range inputs {
		scenario := suiteExplainScenario{
			Name:        input.ScenarioName,
			File:        input.ScenarioPath,
			Tags:        input.Scenario.Metadata.Tags,
			Binding:     input.BindingPath,
			Namespace:   input.Namespace,
			KubeContext: input.KubeContext,
		}
		for _, step := range input.Scenario.Spec.StepInvocations {
			scenario.Steps = append(scenario.Steps, suiteExplainStep{Kind: step.Kind, Text: step.Text})
		}
		if input.Integration != nil {
			for _, app := range input.Integration.Spec.HelmApps {
				namespace := app.Namespace
				if namespace == "" {
					namespace = input.Namespace
				}
				scenario.HelmApps = append(scenario.HelmApps, suitePlanHelmApp{Name: app.Name, Chart: app.Chart, Namespace: namespace, Values: app.Values})
			}
		}
		capabilities := suiteProvidersForInput(input)
		for _, provider := range capabilities {
			scenario.Capabilities = append(scenario.Capabilities, provider)
			providerSet[provider.Provider+"\x00"+provider.OperationType+"\x00"+provider.BindingKind] = provider
		}
		loweredByID := map[string]workspace.LoweredOperation{}
		if registry, err := workspace.NewProviderRegistryWithProviders(input.Providers); err == nil {
			if lowered, err := workspace.LowerOperations(input, registry); err == nil {
				for _, operation := range lowered {
					loweredByID[operation.OperationID] = operation
				}
			}
		}
		for _, op := range input.Scenario.Spec.Operations {
			explained := suiteExplainOperation{ID: op.ID, Type: op.Type, After: op.After}
			if lowered, ok := loweredByID[op.ID]; ok {
				explained.Provider = lowered.Provider
				explained.BindingKind = lowered.Binding.Kind
				explained.BindingName = lowered.Binding.Name
			}
			switch op.Type {
			case "mqtt.publish":
				explained.Topic = op.MQTT.Topic
				explained.PayloadTemplateRef = op.MQTT.PayloadTemplateRef
				explained.CorrelationID = op.MQTT.CorrelationID
			case "rabbitmq.publish":
				explained.Exchange = op.RabbitMQ.Exchange
				explained.RoutingKey = op.RabbitMQ.RoutingKey
				explained.PayloadTemplateRef = op.RabbitMQ.PayloadTemplateRef
				explained.CorrelationID = op.RabbitMQ.CorrelationID
			case "rabbitmq.expect":
				explained.Queue = op.RabbitMQ.Queue
				explained.CorrelationID = op.RabbitMQ.CorrelationID
				explained.MatcherCount = len(op.RabbitMQ.Match)
			case "redpanda.contains":
				explained.TopicRef = op.Redpanda.TopicRef
				explained.CorrelationID = op.Redpanda.CorrelationID
				explained.MatcherCount = len(op.Redpanda.Match)
			case "graphql.expect":
				explained.QueryRef = op.GraphQL.QueryRef
				explained.VariableCount = len(op.GraphQL.Variables)
				explained.MatcherCount = len(op.GraphQL.Match)
			case "mongodb.expect":
				explained.Collection = op.MongoDB.Collection
				explained.CorrelationID = op.MongoDB.CorrelationID
				explained.MatcherCount = len(op.MongoDB.Match)
			case "postgresql.expect":
				explained.ArgCount = len(op.Postgres.Args)
				explained.CorrelationID = op.Postgres.CorrelationID
				explained.MatcherCount = len(op.Postgres.Match)
			}
			scenario.Operations = append(scenario.Operations, explained)
		}
		out.Scenarios = append(out.Scenarios, scenario)
	}
	out.Providers = sortedSuiteProviders(providerSet)
	return out
}

func suiteOutputRoot(resolved workspace.ResolvedScenarioSuite, flags suiteFlags) string {
	if flags.outPath != "" {
		return flags.outPath
	}
	if resolved.Suite.Spec.WorkspaceDir != "" {
		if filepath.IsAbs(resolved.Suite.Spec.WorkspaceDir) {
			return resolved.Suite.Spec.WorkspaceDir
		}
		return filepath.Join(filepath.Dir(resolved.Path), resolved.Suite.Spec.WorkspaceDir)
	}
	return filepath.Join(filepath.Dir(resolved.Path), "generated", workspace.DNSLabel(resolved.Suite.Metadata.Name))
}

func loadSuiteInputs(resolved workspace.ResolvedScenarioSuite, flags suiteFlags) ([]workspace.Inputs, error) {
	var out []workspace.Inputs
	catalogs, err := workspace.LoadCatalogBundle(resolved.CatalogPaths)
	if err != nil {
		return nil, fmt.Errorf("catalogs: %w", err)
	}
	ordinal := 0
	scenarioRefs := resolved.ScenarioRefs
	if len(scenarioRefs) == 0 {
		for _, scenarioPath := range resolved.ScenarioPaths {
			scenarioRefs = append(scenarioRefs, workspace.ResolvedScenarioRef{
				Path:                   scenarioPath,
				BindingPath:            resolved.BindingPath,
				IntegrationProfilePath: resolved.IntegrationProfilePath,
			})
		}
	}
	for _, scenarioRef := range scenarioRefs {
		scenarioInputs, err := workspace.LoadInputsWithCatalogsManyAndProviders(scenarioRef.Path, scenarioRef.BindingPath, catalogs, resolved.Providers)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", scenarioRef.Path, err)
		}
		for i := range scenarioInputs {
			inputs := scenarioInputs[i]
			inputs.Providers = resolved.Providers
			if len(scenarioRef.Parameters) > 0 {
				if inputs.Binding.Spec.ScenarioParameters == nil {
					inputs.Binding.Spec.ScenarioParameters = map[string]string{}
				}
				for k, v := range scenarioRef.Parameters {
					inputs.Binding.Spec.ScenarioParameters[k] = v
				}
			}
			if len(scenarioRef.Tags) > 0 {
				inputs.Scenario.Metadata.Tags = mergeInputTags(inputs.Scenario.Metadata.Tags, scenarioRef.Tags)
			}
			if !matchesSuiteTagFilters(inputs.Scenario.Metadata.Tags, flags.includeTags, flags.excludeTags) {
				continue
			}
			if scenarioRef.IntegrationProfilePath != "" {
				profile, err := workspace.LoadIntegrationProfile(scenarioRef.IntegrationProfilePath)
				if err != nil {
					return nil, fmt.Errorf("integration profile: %w", err)
				}
				inputs.Integration = &profile
				inputs.IntegrationProfilePath = scenarioRef.IntegrationProfilePath
			}
			inputs.CatalogPaths = resolved.CatalogPaths
			applySuiteOverrides(&inputs, flags, ordinal)
			if err := workspace.ValidateRuntimeInputs(inputs); err != nil {
				return nil, fmt.Errorf("%s:%s: %w", scenarioRef.Path, inputs.ScenarioName, err)
			}
			out = append(out, inputs)
			ordinal++
		}
	}
	if len(out) == 0 && (len(flags.includeTags) > 0 || len(flags.excludeTags) > 0) {
		return nil, fmt.Errorf("suite tag filters matched no scenarios")
	}
	return out, nil
}

func mergeInputTags(base, extra []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, tag := range append(append([]string{}, base...), extra...) {
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func matchesSuiteTagFilters(tags []string, includeTags, excludeTags []string) bool {
	tagSet := map[string]struct{}{}
	for _, tag := range tags {
		tagSet[tag] = struct{}{}
	}
	for _, tag := range excludeTags {
		if _, ok := tagSet[tag]; ok {
			return false
		}
	}
	for _, tag := range includeTags {
		if _, ok := tagSet[tag]; !ok {
			return false
		}
	}
	return true
}

func applySuiteOverrides(inputs *workspace.Inputs, flags suiteFlags, ordinal int) {
	if flags.runID != "" {
		inputs.RunID = fmt.Sprintf("%s-%02d", flags.runID, ordinal+1)
	}
	if flags.kubeContext != "" {
		inputs.KubeContext = flags.kubeContext
		inputs.Binding.Spec.KubeContext = flags.kubeContext
	}
	if flags.namespace != "" {
		inputs.Namespace = flags.namespace
		inputs.Binding.Spec.Namespace = flags.namespace
	}
	if flags.probeImage != "" {
		inputs.Binding.Spec.Probe.Image = flags.probeImage
	}
	if flags.probeImagePullPolicy != "" {
		inputs.Binding.Spec.Probe.ImagePullPolicy = flags.probeImagePullPolicy
	}
	if flags.repoRoot != "" {
		inputs.RepoRoot = flags.repoRoot
	}
}

func generateSuiteWorkspaces(outRoot string, resolved workspace.ResolvedScenarioSuite, inputs []workspace.Inputs, stdout io.Writer) ([]string, error) {
	var workspaces []string
	used := map[string]struct{}{}
	for i, input := range inputs {
		name := workspace.DNSLabel(input.ScenarioName)
		if _, ok := used[name]; ok {
			name = fmt.Sprintf("%s-%02d", name, i+1)
		}
		used[name] = struct{}{}
		workspacePath := filepath.Join(outRoot, name)
		if err := workspace.Generate(workspacePath, input); err != nil {
			return nil, fmt.Errorf("%s:%s: %w", input.ScenarioPath, input.ScenarioName, err)
		}
		fmt.Fprintf(stdout, "workspace written: %s\n", workspacePath)
		workspaces = append(workspaces, workspacePath)
	}
	return workspaces, nil
}

func validateOutputDir(path string) error {
	clean := filepath.Clean(path)
	if clean == "." || clean == string(filepath.Separator) {
		return fmt.Errorf("refusing unsafe output directory %q", path)
	}
	info, err := os.Lstat(clean)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing output directory %q: existing path is a symlink", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("refusing output directory %q: existing path is not a directory", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("refusing output directory %q: %w", path, err)
	}
	return nil
}

func runInit(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "scenario-repo" {
		return fmt.Errorf("init requires scenario-repo")
	}
	fs := flag.NewFlagSet("init scenario-repo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", ".", "scenario repository directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "init scenario-repo"); err != nil {
		return err
	}
	if strings.TrimSpace(*dir) == "" {
		return fmt.Errorf("init scenario-repo requires a non-empty --dir")
	}
	root := filepath.Clean(*dir)
	if err := ensureSafeDirectory(root, 0o755); err != nil {
		return err
	}
	for _, path := range []string{
		filepath.Join(root, ".schemas"),
		filepath.Join(root, ".vscode"),
		filepath.Join(root, ".github", "workflows"),
		filepath.Join(root, "ci"),
		filepath.Join(root, "scenarios"),
		filepath.Join(root, "queries"),
		filepath.Join(root, "bindings"),
		filepath.Join(root, "integration"),
		filepath.Join(root, "catalogs"),
		filepath.Join(root, "features"),
		filepath.Join(root, "generated"),
		filepath.Join(root, "reports"),
	} {
		if err := ensureSafeDirectoryUnderRoot(root, path, 0o755); err != nil {
			return err
		}
	}
	files := []struct {
		path    string
		content string
	}{
		{filepath.Join(root, ".gitignore"), scenarioRepoGitignoreTemplate()},
		{filepath.Join(root, "README.md"), scenarioRepoReadmeTemplate()},
		{filepath.Join(root, "Makefile"), scenarioRepoMakefileTemplate()},
		{filepath.Join(root, "suite.yaml"), scenarioRepoSuiteTemplate()},
		{filepath.Join(root, ".vscode", "settings.json"), scenarioRepoVSCodeSettingsTemplate()},
		{filepath.Join(root, ".github", "workflows", "spex.yaml"), scenarioRepoGitHubWorkflowTemplate()},
		{filepath.Join(root, "ci", "spex-validate.sh"), scenarioRepoCIValidateTemplate()},
		{filepath.Join(root, "bindings", "dev.yaml"), scenarioRepoBindingTemplate()},
		{filepath.Join(root, "queries", "latest-device-reading.graphql"), scenarioRepoGraphQLTemplate()},
		{filepath.Join(root, "catalogs", "telemetry-flow.yaml"), scenarioRepoFlowCatalogTemplate()},
		{filepath.Join(root, "catalogs", "telemetry-steps.yaml"), scenarioRepoStepCatalogTemplate()},
		{filepath.Join(root, "features", "mqtt-ingestion.feature"), scenarioRepoFeatureTemplate()},
		{filepath.Join(root, "scenarios", "mqtt-ingestion-basic.yaml"), scenarioRepoScenarioTemplate("mqtt-ingestion-basic")},
		{filepath.Join(root, "scenarios", "mqtt-ingestion-flow.yaml"), scenarioRepoFlowScenarioTemplate()},
	}
	for _, file := range files {
		if err := writeNewFileUnderRoot(root, file.path, file.content); err != nil {
			return err
		}
	}
	if err := chmodRegularFile(filepath.Join(root, "ci", "spex-validate.sh"), 0o755); err != nil {
		return err
	}
	if err := writeScenarioRepoSchemas(root); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "scenario repo initialized: %s\n", root)
	return nil
}

func runNew(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "scenario" {
		return fmt.Errorf("new requires scenario")
	}
	fs := flag.NewFlagSet("new scenario", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", ".", "scenario repository directory")
	name := fs.String("name", "", "scenario name")
	style := fs.String("style", "explicit", "scenario style: explicit, flow, or feature")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch fs.NArg() {
	case 0:
	case 1:
		if *name != "" {
			return fmt.Errorf("new scenario accepts either --name or one positional scenario name, not both")
		}
		*name = fs.Arg(0)
	default:
		return fmt.Errorf("new scenario accepts at most one positional scenario name")
	}
	if *name == "" {
		return fmt.Errorf("new scenario requires --name or a scenario name argument")
	}
	if strings.TrimSpace(*dir) == "" {
		return fmt.Errorf("new scenario requires a non-empty --dir")
	}
	root := filepath.Clean(*dir)
	if err := ensureSafeDirectory(root, 0o755); err != nil {
		return err
	}
	path, content, err := newScenarioFile(root, *name, *style)
	if err != nil {
		return err
	}
	if err := ensureSafeDirectoryUnderRoot(root, filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := writeNewFileUnderRoot(root, path, content); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "scenario written: %s\n", path)
	return nil
}

func newScenarioFile(dir, name, style string) (string, string, error) {
	scenarioName := workspace.DNSLabel(name)
	if scenarioName == "" {
		return "", "", fmt.Errorf("scenario name must contain at least one DNS-label character")
	}
	switch style {
	case "explicit":
		return filepath.Join(dir, "scenarios", scenarioName+".yaml"), scenarioRepoScenarioTemplate(name), nil
	case "flow":
		return filepath.Join(dir, "scenarios", scenarioName+".yaml"), scenarioRepoFlowScenarioTemplate(name), nil
	case "feature":
		return filepath.Join(dir, "features", scenarioName+".feature"), scenarioRepoFeatureTemplate(name), nil
	default:
		return "", "", fmt.Errorf("unsupported scenario style %q; expected explicit, flow, or feature", style)
	}
}

func runExplain(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	scenarioPath := fs.String("scenario", "", "scenario YAML path")
	bindingPath := fs.String("binding", "", "target binding YAML path")
	suitePath := fs.String("suite", "", "scenario suite YAML path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "explain"); err != nil {
		return err
	}
	if *suitePath != "" {
		return runSuiteExplain([]string{"--suite", *suitePath}, stdout)
	}
	if *scenarioPath == "" || *bindingPath == "" {
		return fmt.Errorf("explain requires --scenario and --binding, or --suite")
	}
	inputs, err := workspace.LoadInputs(*scenarioPath, *bindingPath)
	if err != nil {
		return err
	}
	writeInputsExplanation(stdout, inputs)
	return nil
}

func runCatalog(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		catalogUsage(stdout)
		return nil
	}
	switch args[0] {
	case "help", "--help", "-h":
		if len(args) > 1 {
			return fmt.Errorf("catalog help does not accept positional arguments: %s", strings.Join(args[1:], ", "))
		}
		catalogUsage(stdout)
		return nil
	case "list":
		return runCatalogList(args[1:], stdout)
	case "explain":
		return runCatalogExplain(args[1:], stdout)
	case "check":
		return runCatalogCheck(args[1:], stdout)
	case "docs":
		return runCatalogDocs(args[1:], stdout)
	default:
		return fmt.Errorf("unknown catalog command %q", args[0])
	}
}

func catalogUsage(stdout io.Writer) {
	fmt.Fprintln(stdout, `usage: spex catalog <command> [flags]

Catalog commands:
  list     list flows and steps from catalog files or a suite
  explain  show flow parameters, step expressions, and operation counts
  check    validate catalog names and step-expression ambiguity
  docs     generate tester-facing catalog documentation

Examples:
  spex catalog list --suite suite.yaml
  spex catalog check --suite suite.yaml --format json
  spex catalog docs --suite suite.yaml --out reports/catalog.md`)
}

type multiFlag []string

func (m *multiFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func catalogFlags(command string, args []string) (string, []string, string, error) {
	fs := flag.NewFlagSet("catalog "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	suitePath := fs.String("suite", "", "scenario suite YAML path")
	format := fs.String("format", "text", "output format: text or json")
	var catalogs multiFlag
	fs.Var(&catalogs, "catalog", "catalog YAML path, repeatable")
	if err := fs.Parse(args); err != nil {
		return "", nil, "", err
	}
	if err := rejectPositionalArgs(fs, "catalog "+command); err != nil {
		return "", nil, "", err
	}
	if *suitePath == "" && len(catalogs) == 0 {
		return "", nil, "", fmt.Errorf("catalog %s requires --suite or --catalog", command)
	}
	switch *format {
	case "text", "json":
		return *suitePath, catalogs, *format, nil
	default:
		return "", nil, "", fmt.Errorf("catalog %s --format must be text or json", command)
	}
}

func loadCatalogsForCommand(command string, args []string) (workspace.CatalogBundle, string, error) {
	suitePath, catalogs, format, err := catalogFlags(command, args)
	if err != nil {
		return workspace.CatalogBundle{}, "", err
	}
	if suitePath != "" {
		resolved, err := workspace.LoadScenarioSuite(suitePath)
		if err != nil {
			return workspace.CatalogBundle{}, "", fmt.Errorf("suite: %w", err)
		}
		catalogs = append(catalogs, resolved.CatalogPaths...)
	}
	bundle, err := workspace.LoadCatalogBundle(catalogs)
	return bundle, format, err
}

type bundleCommandOutput struct {
	Bundles []bundleSummary `json:"bundles"`
}

type bundleSummary struct {
	Name           string                    `json:"name"`
	Capabilities   []bundleCapabilitySummary `json:"capabilities"`
	BindingSchemas []string                  `json:"bindingSchemas"`
}

type bundleCapabilitySummary struct {
	Type         string            `json:"type"`
	BindingKind  string            `json:"bindingKind"`
	InputSchema  bool              `json:"inputSchema"`
	ResultSchema bool              `json:"resultSchema"`
	Image        string            `json:"image,omitempty"`
	Command      []string          `json:"command,omitempty"`
	InputPath    string            `json:"inputPath,omitempty"`
	OutputPath   string            `json:"outputPath,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
}

func runBundle(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("bundle requires subcommand list or explain")
	}
	switch args[0] {
	case "list":
		return runBundleList(args[1:], stdout)
	case "explain":
		return runBundleExplain(args[1:], stdout)
	default:
		return fmt.Errorf("unknown bundle subcommand %q", args[0])
	}
}

func bundleFlags(command string, args []string) (string, string, error) {
	fs := flag.NewFlagSet("bundle "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	suitePath := fs.String("suite", "", "suite YAML path")
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return "", "", err
	}
	if err := rejectPositionalArgs(fs, "bundle "+command); err != nil {
		return "", "", err
	}
	if *suitePath == "" {
		return "", "", fmt.Errorf("bundle %s requires --suite", command)
	}
	switch *format {
	case "text", "json":
		return *suitePath, *format, nil
	default:
		return "", "", fmt.Errorf("bundle %s --format must be text or json", command)
	}
}

func loadBundlesForCommand(command string, args []string) (bundleCommandOutput, string, error) {
	suitePath, format, err := bundleFlags(command, args)
	if err != nil {
		return bundleCommandOutput{}, "", err
	}
	resolved, err := workspace.LoadScenarioSuite(suitePath)
	if err != nil {
		return bundleCommandOutput{}, "", fmt.Errorf("suite: %w", err)
	}
	out := bundleCommandOutput{}
	for _, ref := range resolved.Suite.Spec.BundleRefs {
		if !strings.HasPrefix(ref.Source, "builtin:") {
			continue
		}
		name := strings.TrimPrefix(ref.Source, "builtin:")
		provider, ok := workspace.BuiltInProvider(name)
		if !ok {
			continue
		}
		out.Bundles = append(out.Bundles, bundleSummaryForProvider(provider))
	}
	for _, provider := range resolved.Providers {
		out.Bundles = append(out.Bundles, bundleSummaryForProvider(provider))
	}
	return out, format, nil
}

func bundleSummaryForProvider(provider workspace.Provider) bundleSummary {
	summary := bundleSummary{Name: provider.Name}
	for _, capability := range provider.Capabilities {
		summary.Capabilities = append(summary.Capabilities, bundleCapabilitySummary{
			Type:         capability.Type,
			BindingKind:  capability.BindingKind,
			InputSchema:  capability.InputSchema.Schema != nil || capability.InputSchema.Name != "" || capability.InputSchema.Path != "",
			ResultSchema: capability.ResultSchema.Schema != nil || capability.ResultSchema.Name != "" || capability.ResultSchema.Path != "",
			Image:        capability.Probe.Image,
			Command:      capability.Probe.Command,
			InputPath:    capability.Probe.Input.Path,
			OutputPath:   capability.Probe.Output.Path,
			Env:          bundleEnvSummary(capability.Probe.Env),
		})
	}
	for _, binding := range provider.BindingSchemas {
		summary.BindingSchemas = append(summary.BindingSchemas, binding.Kind)
	}
	return summary
}

func bundleEnvSummary(env map[string]workspace.ProbeEnvSource) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := map[string]string{}
	for name, source := range env {
		switch {
		case source.Value != "":
			out[name] = "value"
		case source.FromBinding != "":
			out[name] = "fromBinding:" + source.FromBinding
		case source.SecretRef != "":
			out[name] = "secretRef:" + source.SecretRef
		default:
			out[name] = "unresolved"
		}
	}
	return out
}

func runBundleList(args []string, stdout io.Writer) error {
	out, format, err := loadBundlesForCommand("list", args)
	if err != nil {
		return err
	}
	if format == "json" {
		return writeBundleJSON(stdout, out)
	}
	if len(out.Bundles) == 0 {
		fmt.Fprintln(stdout, "bundles: none")
		return nil
	}
	fmt.Fprintln(stdout, "bundles:")
	for _, bundle := range out.Bundles {
		fmt.Fprintf(stdout, "  - %s (%d capability(s))\n", bundle.Name, len(bundle.Capabilities))
	}
	return nil
}

func runBundleExplain(args []string, stdout io.Writer) error {
	out, format, err := loadBundlesForCommand("explain", args)
	if err != nil {
		return err
	}
	if format == "json" {
		return writeBundleJSON(stdout, out)
	}
	if len(out.Bundles) == 0 {
		fmt.Fprintln(stdout, "bundles: none")
		return nil
	}
	for _, bundle := range out.Bundles {
		fmt.Fprintf(stdout, "%s\n", bundle.Name)
		fmt.Fprintln(stdout, "  capabilities:")
		for _, capability := range bundle.Capabilities {
			fmt.Fprintf(stdout, "    - %s\n      bindingKind: %s\n      inputSchema: %t\n      resultSchema: %t\n      image: %s\n      command: %s\n      input: %s\n      output: %s\n",
				capability.Type, capability.BindingKind, capability.InputSchema, capability.ResultSchema, capability.Image, strings.Join(capability.Command, " "), capability.InputPath, capability.OutputPath)
			if len(capability.Env) > 0 {
				fmt.Fprintln(stdout, "      env:")
				for _, name := range sortedStringMapKeys(capability.Env) {
					fmt.Fprintf(stdout, "        %s: %s\n", name, capability.Env[name])
				}
			}
		}
		fmt.Fprintln(stdout, "  bindingSchemas:")
		for _, binding := range bundle.BindingSchemas {
			fmt.Fprintf(stdout, "    - %s\n", binding)
		}
	}
	return nil
}

func sortedStringMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeBundleJSON(stdout io.Writer, out bundleCommandOutput) error {
	content, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(content))
	return err
}

func runCatalogList(args []string, stdout io.Writer) error {
	bundle, format, err := loadCatalogsForCommand("list", args)
	if err != nil {
		return err
	}
	if format == "json" {
		return writeCatalogListJSON(stdout, bundle)
	}
	fmt.Fprintln(stdout, "flows:")
	for _, flow := range bundle.Inventory.Flows {
		fmt.Fprintf(stdout, "  - %s (%s)\n", flow.Name, flow.Source)
	}
	fmt.Fprintln(stdout, "steps:")
	for _, step := range bundle.Inventory.Steps {
		fmt.Fprintf(stdout, "  - %s %q (%s)\n", step.Step.Kind, step.Step.Expression, step.Source)
	}
	return nil
}

func runCatalogExplain(args []string, stdout io.Writer) error {
	bundle, format, err := loadCatalogsForCommand("explain", args)
	if err != nil {
		return err
	}
	if format == "json" {
		return writeCatalogExplainJSON(stdout, bundle)
	}
	fmt.Fprintln(stdout, "flows:")
	for _, flow := range bundle.Inventory.Flows {
		fmt.Fprintf(stdout, "\n%s\n  source: %s\n  parameters:", flow.Name, flow.Source)
		if len(flow.Flow.Parameters) == 0 {
			fmt.Fprint(stdout, " none\n")
		} else {
			fmt.Fprintln(stdout)
			keys := make([]string, 0, len(flow.Flow.Parameters))
			for name := range flow.Flow.Parameters {
				keys = append(keys, name)
			}
			sort.Strings(keys)
			for _, name := range keys {
				fmt.Fprintf(stdout, "    - %s: %s\n", name, flow.Flow.Parameters[name])
			}
		}
		writeExpansionCounts(stdout, "  ", flow.Flow.ExpandsTo)
	}
	fmt.Fprintln(stdout, "\nsteps:")
	for _, step := range bundle.Inventory.Steps {
		fmt.Fprintf(stdout, "\n%s %q\n  source: %s\n", step.Step.Kind, step.Step.Expression, step.Source)
		writeExpansionCounts(stdout, "  ", step.Step.Output)
	}
	return nil
}

func writeExpansionCounts(stdout io.Writer, indent string, expansion workspace.CatalogExpansion) {
	fmt.Fprintf(stdout, "%sparameters: %d\n", indent, len(expansion.Parameters))
	fmt.Fprintf(stdout, "%spayloadTemplates: %d\n", indent, len(expansion.PayloadTemplates))
	fmt.Fprintf(stdout, "%sgraphqlQueries: %d\n", indent, len(expansion.GraphQLQueries))
	fmt.Fprintf(stdout, "%soperations: %d\n", indent, len(expansion.Operations))
}

type catalogListOutput struct {
	Flows []catalogFlowListOutput `json:"flows"`
	Steps []catalogStepListOutput `json:"steps"`
}

type catalogFlowListOutput struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

type catalogStepListOutput struct {
	Kind       string `json:"kind"`
	Expression string `json:"expression"`
	Source     string `json:"source"`
}

type catalogExplainOutput struct {
	Flows []catalogFlowExplainOutput `json:"flows"`
	Steps []catalogStepExplainOutput `json:"steps"`
}

type catalogFlowExplainOutput struct {
	Name                 string            `json:"name"`
	Source               string            `json:"source"`
	Parameters           map[string]string `json:"parameters,omitempty"`
	ParameterCount       int               `json:"parameterCount"`
	PayloadTemplateCount int               `json:"payloadTemplateCount"`
	GraphQLQueryCount    int               `json:"graphqlQueryCount"`
	OperationCount       int               `json:"operationCount"`
}

type catalogStepExplainOutput struct {
	Kind                 string `json:"kind"`
	Expression           string `json:"expression"`
	Source               string `json:"source"`
	ParameterCount       int    `json:"parameterCount"`
	PayloadTemplateCount int    `json:"payloadTemplateCount"`
	GraphQLQueryCount    int    `json:"graphqlQueryCount"`
	OperationCount       int    `json:"operationCount"`
}

func writeCatalogListJSON(stdout io.Writer, bundle workspace.CatalogBundle) error {
	out := catalogListOutput{}
	for _, flow := range bundle.Inventory.Flows {
		out.Flows = append(out.Flows, catalogFlowListOutput{Name: flow.Name, Source: flow.Source})
	}
	for _, step := range bundle.Inventory.Steps {
		out.Steps = append(out.Steps, catalogStepListOutput{
			Kind:       step.Step.Kind,
			Expression: step.Step.Expression,
			Source:     step.Source,
		})
	}
	content, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(content))
	return nil
}

func writeCatalogExplainJSON(stdout io.Writer, bundle workspace.CatalogBundle) error {
	out := catalogExplainOutput{}
	for _, flow := range bundle.Inventory.Flows {
		out.Flows = append(out.Flows, catalogFlowExplainOutput{
			Name:                 flow.Name,
			Source:               flow.Source,
			Parameters:           flow.Flow.Parameters,
			ParameterCount:       len(flow.Flow.ExpandsTo.Parameters),
			PayloadTemplateCount: len(flow.Flow.ExpandsTo.PayloadTemplates),
			GraphQLQueryCount:    len(flow.Flow.ExpandsTo.GraphQLQueries),
			OperationCount:       len(flow.Flow.ExpandsTo.Operations),
		})
	}
	for _, step := range bundle.Inventory.Steps {
		out.Steps = append(out.Steps, catalogStepExplainOutput{
			Kind:                 step.Step.Kind,
			Expression:           step.Step.Expression,
			Source:               step.Source,
			ParameterCount:       len(step.Step.Output.Parameters),
			PayloadTemplateCount: len(step.Step.Output.PayloadTemplates),
			GraphQLQueryCount:    len(step.Step.Output.GraphQLQueries),
			OperationCount:       len(step.Step.Output.Operations),
		})
	}
	content, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(content))
	return nil
}

func writeInputsExplanation(stdout io.Writer, inputs workspace.Inputs) {
	fmt.Fprintf(stdout, "\nscenario: %s\n", inputs.ScenarioName)
	fmt.Fprintf(stdout, "scenarioFile: %s\n", inputs.ScenarioPath)
	if len(inputs.Scenario.Metadata.Tags) > 0 {
		fmt.Fprintf(stdout, "tags: %s\n", strings.Join(inputs.Scenario.Metadata.Tags, ", "))
	}
	fmt.Fprintf(stdout, "bindingFile: %s\n", inputs.BindingPath)
	fmt.Fprintf(stdout, "namespace: %s\n", inputs.Namespace)
	if inputs.KubeContext != "" {
		fmt.Fprintf(stdout, "kubeContext: %s\n", inputs.KubeContext)
	}
	if inputs.Integration != nil {
		if len(inputs.Integration.Spec.HelmApps) > 0 {
			fmt.Fprintln(stdout, "helmApps:")
			for _, app := range inputs.Integration.Spec.HelmApps {
				namespace := app.Namespace
				if namespace == "" {
					namespace = inputs.Namespace
				}
				fmt.Fprintf(stdout, "  - %s: %s -> namespace %s\n", app.Name, app.Chart, namespace)
			}
		}
	}
	if len(inputs.Scenario.Spec.StepInvocations) > 0 {
		fmt.Fprintln(stdout, "steps:")
		for _, step := range inputs.Scenario.Spec.StepInvocations {
			fmt.Fprintf(stdout, "  - %s %s\n", step.Kind, step.Text)
		}
	}
	if providers := suiteProvidersForInput(inputs); len(providers) > 0 {
		fmt.Fprintln(stdout, "capabilities:")
		for _, provider := range providers {
			fmt.Fprintf(stdout, "  - %s: %s bindingKind=%s bindings=%s\n", provider.Provider, provider.OperationType, provider.BindingKind, strings.Join(provider.BindingNames, ","))
		}
	}
	fmt.Fprintln(stdout, "operations:")
	for _, op := range inputs.Scenario.Spec.Operations {
		switch op.Type {
		case "mqtt.publish":
			fmt.Fprintf(stdout, "  - %s: MQTT publish topic=%s payloadTemplate=%s correlationId=%s\n", op.ID, op.MQTT.Topic, op.MQTT.PayloadTemplateRef, op.MQTT.CorrelationID)
		case "rabbitmq.publish":
			fmt.Fprintf(stdout, "  - %s: RabbitMQ publish exchange=%s routingKey=%s payloadTemplate=%s correlationId=%s\n", op.ID, op.RabbitMQ.Exchange, op.RabbitMQ.RoutingKey, op.RabbitMQ.PayloadTemplateRef, op.RabbitMQ.CorrelationID)
		case "rabbitmq.expect":
			fmt.Fprintf(stdout, "  - %s: RabbitMQ expect queue=%s correlationId=%s matchers=%d\n", op.ID, op.RabbitMQ.Queue, op.RabbitMQ.CorrelationID, len(op.RabbitMQ.Match))
		case "redpanda.contains":
			fmt.Fprintf(stdout, "  - %s: Redpanda contains topicRef=%s correlationId=%s matchers=%d\n", op.ID, op.Redpanda.TopicRef, op.Redpanda.CorrelationID, len(op.Redpanda.Match))
		case "graphql.expect":
			fmt.Fprintf(stdout, "  - %s: GraphQL expect queryRef=%s variables=%d matchers=%d\n", op.ID, op.GraphQL.QueryRef, len(op.GraphQL.Variables), len(op.GraphQL.Match))
		case "mongodb.expect":
			fmt.Fprintf(stdout, "  - %s: MongoDB expect collection=%s correlationId=%s matchers=%d\n", op.ID, op.MongoDB.Collection, op.MongoDB.CorrelationID, len(op.MongoDB.Match))
		case "postgresql.expect":
			fmt.Fprintf(stdout, "  - %s: PostgreSQL expect args=%d correlationId=%s matchers=%d\n", op.ID, len(op.Postgres.Args), op.Postgres.CorrelationID, len(op.Postgres.Match))
		default:
			fmt.Fprintf(stdout, "  - %s: %s\n", op.ID, op.Type)
		}
	}
}

func writeNewFile(path, content string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to overwrite existing file %s: path is a symlink", path)
		}
		return fmt.Errorf("refusing to overwrite existing file %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("refusing to overwrite existing file %s", path)
		}
		return err
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.WriteString(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	keep = true
	return syncDirectory(filepath.Dir(path))
}

func writeNewFileUnderRoot(root, path, content string) error {
	if err := ensureSafeDirectoryUnderRoot(root, filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeNewFile(path, content)
}

func chmodRegularFile(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", filepath.Base(path))
	}
	return os.Chmod(path, mode)
}

func writeScenarioRepoSchemas(dir string) error {
	names, err := schemaNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		content, err := schemaFS.ReadFile(filepath.ToSlash(filepath.Join("schemas", name+".schema.json")))
		if err != nil {
			return err
		}
		if err := writeNewFileUnderRoot(dir, filepath.Join(dir, ".schemas", name+".schema.json"), string(content)); err != nil {
			return err
		}
	}
	return nil
}

func scenarioRepoVSCodeSettingsTemplate() string {
	return `{
  "yaml.schemas": {
    "./.schemas/scenario-suite.schema.json": [
      "suite.yaml",
      "suites/*.yaml"
    ],
    "./.schemas/scenario.schema.json": [
      "scenarios/*.yaml",
      "scenarios/**/*.yaml"
    ],
    "./.schemas/target-binding.schema.json": [
      "bindings/*.yaml",
      "bindings/**/*.yaml"
    ],
    "./.schemas/integration-profile.schema.json": [
      "integration/*.yaml",
      "integration/**/*.yaml"
    ],
    "./.schemas/integration-bundle.schema.json": [
      "bundles/*/bundle.yaml",
      "bundles/**/*.bundle.yaml"
    ],
    "./.schemas/flow-catalog.schema.json": [
      "catalogs/*flow*.yaml",
      "catalogs/**/*flow*.yaml"
    ],
    "./.schemas/step-catalog.schema.json": [
      "catalogs/*step*.yaml",
      "catalogs/**/*step*.yaml"
    ]
  }
}
`
}

func scenarioRepoGitignoreTemplate() string {
	return `generated/
reports/
.spex/
*.tmp
`
}

func scenarioRepoMakefileTemplate() string {
	return `SPEX ?= spex
SUITE ?= suite.yaml
OUT ?= generated/device-acceptance
PRODUCTION_ARTIFACTS ?= reports generated
REQUIRE_PINNED_IMAGES ?= false

.PHONY: help doctor doctor-json production-check validate plan explain catalog catalog-docs compile ci run clean schemas

help:
	@printf '%s\n' \
	  'spex scenario repository targets:' \
	  '  make validate      validate suite/scenario/binding/catalog inputs' \
	  '  make plan          write suite inventory and execution plan reports' \
	  '  make explain       write resolved scenario operation reports' \
	  '  make catalog       inspect and validate reusable catalogs' \
	  '  make catalog-docs  write tester-facing catalog documentation' \
	  '  make compile       generate KUTTL workspaces without running them' \
	  '  make ci            run non-cluster validation and compile checks' \
	  '  make doctor        run host and suite preflight checks' \
	  '  make doctor-json   write reports/doctor.json' \
	  '  make production-check fail on mutable refs, leaked secret values, and optionally unpinned images' \
	  '  make run           run the suite against the configured target' \
	  '  make clean         delete generated KUTTL runtime resources' \
	  '  make schemas       refresh local JSON Schema files'

doctor:
	$(SPEX) doctor --suite $(SUITE)

doctor-json:
	mkdir -p reports
	$(SPEX) doctor --suite $(SUITE) --format json > reports/doctor.json

production-check:
	$(SPEX) doctor --suite $(SUITE) --skip-host-tools --require-pinned-git-refs $(if $(filter true,$(REQUIRE_PINNED_IMAGES)),--require-pinned-images,) $(foreach dir,$(PRODUCTION_ARTIFACTS),--scan-artifacts $(dir)) --format json

validate:
	$(SPEX) suite validate --suite $(SUITE)

plan:
	mkdir -p reports
	$(SPEX) suite list --suite $(SUITE) --format json > reports/suite-list.json
	$(SPEX) suite plan --suite $(SUITE)
	$(SPEX) suite plan --suite $(SUITE) --format json > reports/suite-plan.json

explain:
	mkdir -p reports
	$(SPEX) suite explain --suite $(SUITE)
	$(SPEX) suite explain --suite $(SUITE) > reports/suite-explain.txt
	$(SPEX) suite explain --suite $(SUITE) --format json > reports/suite-explain.json

catalog:
	mkdir -p reports
	$(SPEX) catalog list --suite $(SUITE)
	$(SPEX) catalog list --suite $(SUITE) --format json > reports/catalog-list.json
	$(SPEX) catalog explain --suite $(SUITE) --format json > reports/catalog-explain.json
	$(SPEX) catalog check --suite $(SUITE)
	$(SPEX) catalog check --suite $(SUITE) --format json > reports/catalog-check.json

catalog-docs:
	mkdir -p reports
	$(SPEX) catalog docs --suite $(SUITE) --out reports/catalog.md

compile:
	$(SPEX) suite compile --suite $(SUITE) --out $(OUT)

ci:
	./ci/spex-validate.sh

run:
	$(SPEX) suite run --suite $(SUITE) --out $(OUT)

clean:
	find generated -mindepth 1 -maxdepth 1 -type d -exec $(SPEX) clean --workspace {} --all \;

schemas:
	$(SPEX) schema show scenario-suite > .schemas/scenario-suite.schema.json
	$(SPEX) schema show scenario > .schemas/scenario.schema.json
	$(SPEX) schema show target-binding > .schemas/target-binding.schema.json
	$(SPEX) schema show integration-profile > .schemas/integration-profile.schema.json
	$(SPEX) schema show integration-bundle > .schemas/integration-bundle.schema.json
	$(SPEX) schema show flow-catalog > .schemas/flow-catalog.schema.json
	$(SPEX) schema show step-catalog > .schemas/step-catalog.schema.json
`
}

func scenarioRepoCIValidateTemplate() string {
	return `#!/bin/sh
set -eu

: "${SPEX:=spex}"
: "${SUITE:=suite.yaml}"
: "${OUT:=generated/ci}"
: "${SPEX_PRODUCTION_CHECK:=false}"
: "${SPEX_REQUIRE_PINNED_IMAGES:=false}"

if ! command -v "$SPEX" >/dev/null 2>&1; then
  echo "missing spex binary. Set SPEX=/path/to/spex or install it on PATH." >&2
  exit 127
fi

"$SPEX" version
mkdir -p reports
"$SPEX" schema list --format json > reports/schema-list.json
"$SPEX" suite validate --suite "$SUITE"
"$SPEX" suite list --suite "$SUITE"
"$SPEX" suite list --suite "$SUITE" --format json > reports/suite-list.json
"$SPEX" suite plan --suite "$SUITE"
"$SPEX" suite plan --suite "$SUITE" --format json > reports/suite-plan.json
"$SPEX" suite explain --suite "$SUITE"
"$SPEX" suite explain --suite "$SUITE" > reports/suite-explain.txt
"$SPEX" suite explain --suite "$SUITE" --format json > reports/suite-explain.json
"$SPEX" catalog list --suite "$SUITE"
"$SPEX" catalog list --suite "$SUITE" --format json > reports/catalog-list.json
"$SPEX" catalog explain --suite "$SUITE" --format json > reports/catalog-explain.json
"$SPEX" catalog check --suite "$SUITE"
"$SPEX" catalog check --suite "$SUITE" --format json > reports/catalog-check.json
"$SPEX" catalog docs --suite "$SUITE" --out reports/catalog.md
"$SPEX" suite compile --suite "$SUITE" --out "$OUT"
if [ "$SPEX_PRODUCTION_CHECK" = "true" ]; then
  PINNED_IMAGE_ARG=
  if [ "$SPEX_REQUIRE_PINNED_IMAGES" = "true" ]; then
    PINNED_IMAGE_ARG=--require-pinned-images
  fi
  "$SPEX" doctor --suite "$SUITE" --skip-host-tools --require-pinned-git-refs $PINNED_IMAGE_ARG --scan-artifacts reports --scan-artifacts "$OUT" --format json > reports/production-check.json
fi
`
}

func scenarioRepoGitHubWorkflowTemplate() string {
	return `name: spex

on:
  pull_request:
  push:
    branches:
      - main

permissions:
  contents: read

concurrency:
  group: spex-${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  validate:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - uses: actions/checkout@v4
        with:
          persist-credentials: false

      # Install or provide the spex binary before this step.
      # Common options:
      # - use an internal setup action
      # - download a released binary from your artifact registry
      # - run this job in a container image that already contains spex
      - name: Validate scenario repository
        run: ./ci/spex-validate.sh

      # Enable this once refs are pinned and generated artifacts should be scanned
      # for exact SPEX secret values in CI. Provide representative SPEX_*
      # secret environment variables so the scanner has real values to search for.
      # - name: Production gate
      #   run: SPEX_PRODUCTION_CHECK=true ./ci/spex-validate.sh

      # Live jobs should remove generated kubeconfigs before uploading artifacts:
      # find generated -name kubeconfig -type f -delete

      - name: Upload spex reports
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: spex-reports
          path: reports/
          if-no-files-found: ignore
          retention-days: 14
`
}

func scenarioRepoReadmeTemplate() string {
	return `# spex scenario repository

This repository contains tester-owned scenario intent and pipeline configuration. Generated KUTTL workspaces should stay out of source control.

The scaffold includes three equivalent authoring styles:

- scenarios/mqtt-ingestion-basic.yaml: explicit ScenarioModel operations
- scenarios/mqtt-ingestion-flow.yaml: reusable FlowCatalog expansion
- features/mqtt-ingestion.feature: constrained Gherkin backed by StepCatalog

## Local workflow

~~~sh
make help
make validate
make doctor-json
make explain
make catalog
make compile
~~~

The same non-cluster checks are available for CI:

~~~sh
make ci
~~~

make ci writes inspection artifacts under reports/, including schema-list.json, suite-list.json, suite-plan.json, suite-explain.txt, suite-explain.json, catalog-list.json, catalog-explain.json, catalog-check.json, and catalog.md. catalog-explain.json and catalog.md include expansion counts for parameters, payload templates, GraphQL queries, and operations, so parameter-only Given setup steps are visible. make doctor-json writes reports/doctor.json separately because host preflight checks may depend on tools and environment variables that are not available in every validation job.

Before promoting a pipeline beyond a pilot, run make production-check after validation/compile. It fails mutable Git refs and scans reports/generated artifacts for exact SPEX secret values without printing those values.

Run the suite when the target binding points at a reachable cluster:

~~~sh
make run
~~~

## What testers usually edit

- suite.yaml
- scenarios/*.yaml
- features/*.feature
- queries/*.graphql

## What platform owners usually provide

- bindings/*.yaml
- integration/*.yaml
- catalogs/*.yaml
- Kubernetes Secrets in target namespaces

## External references

bindingRef, integrationProfileRef, and catalogRefs may point at local files or Git-style refs such as platform-targets/bindings/dev.yaml@v1.2.3. Pin CI refs to immutable tags or commit SHAs; mutable refs such as @main should fail the production gate.

## Custom applications

Custom applications belong in an IntegrationProfile, not in scenario files. Add Helm charts with values files and set overrides:

~~~yaml
apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  extends:
    - platform-targets/integration/local-kind.yaml@v1.2.3
  helmApps:
    - name: my-service
      chart: my-service
      repo: https://charts.example.com/team
      namespace: platform
      values:
        - integration/values/my-service.yaml
      set:
        image.repository: registry.example.com/my-service
        image.digest: sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
      wait: true
      timeout: 5m
~~~

For OCI archives or local chart directories, omit repo and put the full chart reference in chart.

## Schemas

The .schemas directory and .vscode/settings.json are generated for YAML editor support. Refresh schema files with:

~~~sh
make schemas
~~~

The generated schemas intentionally catch common authoring mistakes early, including empty refs, empty matcher arrays, duplicate report formats, and empty secret key maps.

## Pipeline

The scaffold includes ci/spex-validate.sh and .github/workflows/spex.yaml. The workflow intentionally runs validation, explanation, catalog discovery, and compile only, then uploads reports/ as the spex-reports artifact. Live cluster execution should be added by the platform team once target bindings, secrets, and cluster provisioning are defined for that environment.

Set SPEX_PRODUCTION_CHECK=true for ci/spex-validate.sh when you want the non-cluster production gate to write reports/production-check.json.
`
}

func scenarioRepoSuiteTemplate() string {
	return `apiVersion: spex.suite.v0.1
kind: ScenarioSuite
metadata:
  name: device-acceptance
spec:
  bindingRef: bindings/dev.yaml
  catalogRefs:
    - catalogs/telemetry-flow.yaml
    - catalogs/telemetry-steps.yaml
  scenarios:
    - scenarios/**/*.yaml
    - features/**/*.feature
  workspaceDir: generated/device-acceptance
  failFast: false
  reports:
    outputDir: reports
    # Omit format or leave it empty to write all supported formats.
    # Each listed format must be unique.
    format:
      - yaml
      - json
      - junit
`
}

func scenarioRepoFlowCatalogTemplate() string {
	return `apiVersion: spex.catalog.v0.1
kind: FlowCatalog
metadata:
  name: telemetry-flow
spec:
  flows:
    mqttToRedpandaToGraphql:
      parameters:
        tenantId: string
        deviceId: string
        value: number
      expandsTo:
        payloadTemplates:
          valid-energy-reading-{id}:
            contentType: application/json
            body: |
              {
                "scenarioRunId": "${scenarioRunId}",
                "correlationId": "${correlationId}",
                "tenantId": "{tenantId}",
                "deviceId": "{deviceId}",
                "measurement": "energy.total",
                "value": {value},
                "unit": "kWh"
              }
        graphqlQueries:
          latest-device-reading:
            file: ../queries/latest-device-reading.graphql
        operations:
          - id: publish-{id}
            type: mqtt.publish
            mqtt:
              topic: telemetry/{tenantId}/{deviceId}/readings
              payloadTemplateRef: valid-energy-reading-{id}
              correlationId: "{id}"
          - id: assert-redpanda-{id}
            type: redpanda.contains
            after: publish-{id}
            redpanda:
              topicRef: normalized-readings
              correlationId: "{id}"
              timeout: 60s
              match:
                - path: $.scenarioRunId
                  equalsString: ${scenarioRunId}
                - path: $.correlationId
                  equalsString: "{id}"
                - path: $.value
                  equalsNumber: "{value}"
          - id: assert-graphql-{id}
            type: graphql.expect
            after: publish-{id}
            graphql:
              queryRef: latest-device-reading
              variables:
                scenarioRunId: ${scenarioRunId}
                correlationId: "{id}"
                deviceId: "{deviceId}"
              timeout: 60s
              match:
                - path: $.data.latestDeviceReading.scenarioRunId
                  equalsString: ${scenarioRunId}
                - path: $.data.latestDeviceReading.correlationId
                  equalsString: "{id}"
                - path: $.data.latestDeviceReading.value
                  equalsNumber: "{value}"
`
}

func scenarioRepoStepCatalogTemplate() string {
	return `apiVersion: spex.catalog.v0.1
kind: StepCatalog
metadata:
  name: telemetry-steps
spec:
  steps:
    - kind: given
      expression: tenant "{tenantId}"
      output:
        parameters:
          tenantId:
            type: string
            default: "{tenantId}"
    - kind: given
      expression: device "{deviceId}"
      output:
        parameters:
          deviceId:
            type: string
            default: "{deviceId}"
    - kind: when
      expression: device "{deviceId}" publishes energy reading {value:number} as "{correlationId}"
      output:
        payloadTemplates:
          valid-energy-reading-{correlationId}:
            contentType: application/json
            body: |
              {
                "scenarioRunId": "${scenarioRunId}",
                "correlationId": "${correlationId}",
                "tenantId": "${param.tenantId}",
                "deviceId": "{deviceId}",
                "measurement": "energy.total",
                "value": {value},
                "unit": "kWh"
              }
        graphqlQueries:
          latest-device-reading:
            file: ../queries/latest-device-reading.graphql
        operations:
          - id: publish-{correlationId}
            type: mqtt.publish
            mqtt:
              topic: telemetry/${param.tenantId}/{deviceId}/readings
              payloadTemplateRef: valid-energy-reading-{correlationId}
              correlationId: "{correlationId}"
    - kind: then
      expression: Redpanda contains reading "{correlationId}" with value {value:number}
      output:
        operations:
          - id: assert-redpanda-{correlationId}
            type: redpanda.contains
            after: publish-{correlationId}
            redpanda:
              topicRef: normalized-readings
              correlationId: "{correlationId}"
              timeout: 60s
              match:
                - path: $.scenarioRunId
                  equalsString: ${scenarioRunId}
                - path: $.correlationId
                  equalsString: "{correlationId}"
                - path: $.value
                  equalsNumber: "{value}"
    - kind: then
      expression: GraphQL returns reading "{correlationId}" for device "{deviceId}" with value {value:number}
      output:
        operations:
          - id: assert-graphql-{correlationId}
            type: graphql.expect
            after: publish-{correlationId}
            graphql:
              queryRef: latest-device-reading
              variables:
                scenarioRunId: ${scenarioRunId}
                correlationId: "{correlationId}"
                deviceId: "{deviceId}"
              timeout: 60s
              match:
                - path: $.data.latestDeviceReading.scenarioRunId
                  equalsString: ${scenarioRunId}
                - path: $.data.latestDeviceReading.correlationId
                  equalsString: "{correlationId}"
                - path: $.data.latestDeviceReading.value
                  equalsNumber: "{value}"
`
}

func scenarioRepoFeatureTemplate(name ...string) string {
	title := "MQTT reading reaches Redpanda and GraphQL"
	if len(name) > 0 && strings.TrimSpace(name[0]) != "" {
		title = strings.TrimSpace(name[0])
	}
	return fmt.Sprintf(`@smoke @mqtt
Feature: MQTT ingestion

  @graphql
  Scenario: %s
    Given tenant "tenant-dev"
    And device "device-dev-1"
    When device "device-dev-1" publishes energy reading 42.5 as "reading-1"
    Then Redpanda contains reading "reading-1" with value 42.5
    And GraphQL returns reading "reading-1" for device "device-dev-1" with value 42.5
`, title)
}

func scenarioRepoFlowScenarioTemplate(name ...string) string {
	scenarioName := "mqtt-ingestion-flow"
	if len(name) > 0 && workspace.DNSLabel(name[0]) != "" {
		scenarioName = workspace.DNSLabel(name[0])
	}
	return fmt.Sprintf(`apiVersion: spex.scenario.v0.1
kind: Scenario
metadata:
  name: %s
spec:
  description: Valid MQTT telemetry reaches Redpanda and GraphQL via a reusable flow.
  parameters:
    tenantId:
      type: string
      default: tenant-dev
    deviceId:
      type: string
      default: device-dev-1
  defaults:
    timeout: 60s
    pollInterval: 1s
  correlation:
    scenarioRunId: auto
    strategy: payloadTemplate
  use:
    - flow: mqttToRedpandaToGraphql
      id: reading-1
      with:
        tenantId: tenant-dev
        deviceId: device-dev-1
        value: "42.5"
`, scenarioName)
}

func scenarioRepoBindingTemplate() string {
	return `apiVersion: spex.binding.v0.1
kind: TargetBinding
metadata:
  name: dev
spec:
  kubeContext: dev
  namespace: spex-test
  scenarioParameters:
    tenantId: tenant-dev
    deviceId: device-dev-1
  rbac:
    create: true
  probe:
    image: spex-probe:dev
    imagePullPolicy: IfNotPresent
    serviceAccountName: spex-probe
  secrets:
    mqtt-credentials:
      type: kubernetesSecret
      name: mqtt-probe-credentials
      keys:
        username: username
        password: password
    graphql-token:
      type: kubernetesSecret
      name: graphql-probe-credentials
      keys:
        token: token
  mqtt:
    brokerURL: tcp://emqx.platform.svc.cluster.local:1883
    clientIdPrefix: spex
    credentialsRef: mqtt-credentials
  redpanda:
    brokers: redpanda.streaming.svc.cluster.local:9092
    topics:
      normalized-readings:
        name: ingestion.normalized-readings
        allowOffsetSnapshot: true
        allowCompacted: false
  graphql:
    endpoint: http://graphql-api.application.svc.cluster.local/graphql
    credentialsRef: graphql-token
`
}

func scenarioRepoGraphQLTemplate() string {
	return `query LatestDeviceReading($scenarioRunId: String!, $correlationId: String!, $deviceId: String!) {
  latestDeviceReading(
    scenarioRunId: $scenarioRunId
    correlationId: $correlationId
    deviceId: $deviceId
  ) {
    scenarioRunId
    correlationId
    value
  }
}
`
}

func scenarioRepoScenarioTemplate(name string) string {
	scenarioName := workspace.DNSLabel(name)
	if scenarioName == "" {
		scenarioName = "mqtt-ingestion-basic"
	}
	return fmt.Sprintf(`apiVersion: spex.scenario.v0.1
kind: Scenario
metadata:
  name: %s
spec:
  description: Valid MQTT telemetry reaches Redpanda and GraphQL.
  parameters:
    tenantId:
      type: string
      default: tenant-dev
    deviceId:
      type: string
      default: device-dev-1
  defaults:
    timeout: 60s
    pollInterval: 1s
  correlation:
    scenarioRunId: auto
    strategy: payloadTemplate
  payloadTemplates:
    valid-energy-reading:
      contentType: application/json
      body: |
        {
          "scenarioRunId": "${scenarioRunId}",
          "correlationId": "${correlationId}",
          "tenantId": "${param.tenantId}",
          "deviceId": "${param.deviceId}",
          "measurement": "energy.total",
          "value": 42.5,
          "unit": "kWh"
        }
  graphqlQueries:
    latest-device-reading:
      file: ../queries/latest-device-reading.graphql
  operations:
    - id: publish-reading-1
      type: mqtt.publish
      mqtt:
        topic: telemetry/${param.tenantId}/${param.deviceId}/readings
        payloadTemplateRef: valid-energy-reading
        correlationId: reading-1
    - id: assert-reading-1-in-redpanda
      type: redpanda.contains
      after: publish-reading-1
      redpanda:
        topicRef: normalized-readings
        correlationId: reading-1
        timeout: 60s
        match:
          - path: $.scenarioRunId
            equalsString: ${scenarioRunId}
          - path: $.correlationId
            equalsString: reading-1
          - path: $.value
            equalsNumber: "42.5"
    - id: assert-reading-1-in-graphql
      type: graphql.expect
      after: publish-reading-1
      graphql:
        queryRef: latest-device-reading
        variables:
          scenarioRunId: ${scenarioRunId}
          correlationId: reading-1
          deviceId: ${param.deviceId}
        timeout: 60s
        match:
          - path: $.data.latestDeviceReading.scenarioRunId
            equalsString: ${scenarioRunId}
          - path: $.data.latestDeviceReading.correlationId
            equalsString: reading-1
          - path: $.data.latestDeviceReading.value
            equalsNumber: "42.5"
`, scenarioName)
}

func runWorkspace(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspacePath := fs.String("workspace", "", "generated workspace directory")
	command := fs.String("command", "kubectl", "KUTTL command executable")
	retainRuntimeResources := fs.Bool("retain-runtime-resources", false, "keep generated Jobs and runtime ConfigMaps after evidence collection")
	collectResourceUsage := fs.Bool("collect-resource-usage", false, "collect best-effort kubectl top pod evidence")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "run"); err != nil {
		return err
	}
	if *workspacePath == "" {
		return fmt.Errorf("run requires --workspace")
	}
	startedAt := time.Now().UTC()
	stepMap, stepMapErr := loadStepMap(*workspacePath)
	if stepMapErr != nil {
		finishedAt := time.Now().UTC()
		class := "workspace_completeness_failure"
		message := strings.TrimSpace(stepMapErr.Error())
		if message == "" {
			message = "failed to load step-map.yaml"
		}
		reportPath, reportErr := WriteReport(ReportInput{
			Workspace:      *workspacePath,
			StartedAt:      startedAt,
			FinishedAt:     finishedAt,
			ScenarioResult: "not_run",
			RunnerResult:   "error",
			FailureClass:   &class,
			FailureMessage: &message,
			KUTTLOutput:    "",
		})
		if reportErr != nil {
			return reportErr
		}
		fmt.Fprintf(stdout, "report written: %s\n", reportPath)
		return stepMapErr
	}
	result := executeKUTTL(*command, *workspacePath, stepMap.Spec.KubeContext, stdout, stderr)
	finishedAt := time.Now().UTC()
	if len(stepMap.Spec.Steps) > 0 && result.ScenarioResult != "not_run" {
		collectEvidence(*command, *workspacePath, stepMap)
		if *collectResourceUsage {
			collectResourceUsageEvidence(*command, *workspacePath, stepMap)
		}
	}
	cleanupErr := error(nil)
	if !*retainRuntimeResources && result.ScenarioResult != "not_run" {
		cleanupErr = cleanupRuntimeResources(*command, *workspacePath, stepMap, stdout)
		if cleanupErr != nil {
			class := "runtime_cleanup_failed"
			message := strings.TrimSpace(cleanupErr.Error())
			if message == "" {
				message = "runtime cleanup failed"
			}
			result.RunnerResult = "error"
			result.FailureClass = &class
			result.FailureMessage = &message
		}
	}
	reportPath, reportErr := WriteReport(ReportInput{
		Workspace:      *workspacePath,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		ScenarioResult: result.ScenarioResult,
		RunnerResult:   result.RunnerResult,
		FailureClass:   result.FailureClass,
		FailureMessage: result.FailureMessage,
		KUTTLOutput:    result.Output,
	})
	if reportErr != nil {
		return reportErr
	}
	fmt.Fprintf(stdout, "report written: %s\n", reportPath)
	if result.Err != nil {
		return result.Err
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	return nil
}

func runClean(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspacePath := fs.String("workspace", "", "generated workspace directory")
	command := fs.String("command", "kubectl", "kubectl executable")
	all := fs.Bool("all", false, "delete static generated ConfigMaps in addition to runtime resources")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "clean"); err != nil {
		return err
	}
	if *workspacePath == "" {
		return fmt.Errorf("clean requires --workspace")
	}
	stepMap, err := loadStepMap(*workspacePath)
	if err != nil {
		return err
	}
	if stepMap.Spec.Namespace == "" || stepMap.Metadata.Scenario == "" {
		return fmt.Errorf("step-map.yaml must include namespace and scenario")
	}
	selector := "spex/owned=true,spex/scenario=" + stepMap.Metadata.Scenario
	commands := runtimeCleanupCommands(stepMap, selector)
	if *all {
		commands = append(commands, []string{"-n", stepMap.Spec.Namespace, "delete", "configmap", "-l", selector + ",spex/static=true", "--ignore-not-found=true"})
	}
	return runCleanupCommands(*command, *workspacePath, stepMap, commands, stdout, "clean completed")
}

func cleanupRuntimeResources(command, workspacePath string, stepMap stepMapFile, stdout io.Writer) error {
	if stepMap.Spec.Namespace == "" || stepMap.Metadata.Scenario == "" {
		return nil
	}
	selector := "spex/owned=true,spex/scenario=" + stepMap.Metadata.Scenario
	return runCleanupCommands(command, workspacePath, stepMap, runtimeCleanupCommands(stepMap, selector), stdout, "")
}

func runtimeCleanupCommands(stepMap stepMapFile, selector string) [][]string {
	return [][]string{
		{"-n", stepMap.Spec.Namespace, "delete", "job", "-l", selector, "--ignore-not-found=true"},
		{"-n", stepMap.Spec.Namespace, "delete", "configmap", "-l", selector + ",spex/runtime=true", "--ignore-not-found=true"},
	}
}

func runCleanupCommands(command, workspacePath string, stepMap stepMapFile, commands [][]string, stdout io.Writer, successMessage string) error {
	for _, args := range commands {
		args = kubectlArgsForWorkspace(workspacePath, stepMap.Spec.KubeContext, args...)
		output, err := runBoundedCommand(maxCleanupOutputSize, command, args...)
		if len(output) > 0 {
			fmt.Fprint(stdout, string(output))
		}
		if err != nil {
			if message := strings.TrimSpace(string(output)); message != "" {
				return fmt.Errorf("%s", message)
			}
			return err
		}
	}
	if successMessage != "" {
		fmt.Fprintln(stdout, successMessage)
	}
	return nil
}

type kuttlResult struct {
	ScenarioResult string
	RunnerResult   string
	FailureClass   *string
	FailureMessage *string
	Output         string
	Err            error
}

func executeKUTTL(command, workspacePath, kubeContext string, stdout, stderr io.Writer) kuttlResult {
	_ = kubeContext
	cmd := exec.Command(command, "kuttl", "test", "--config", "kuttl-test.yaml")
	cmd.Dir = workspacePath
	capture := newLimitedCapture(maxKUTTLOutputSize)
	cmd.Stdout = capture
	cmd.Stderr = capture
	err := cmd.Run()
	output := capture.String()
	if output != "" {
		fmt.Fprint(stdout, output)
	}
	if err == nil {
		return kuttlResult{ScenarioResult: "passed", RunnerResult: "passed", Output: output}
	}
	var execErr *exec.Error
	var pathErr *os.PathError
	if errors.As(err, &execErr) || errors.As(err, &pathErr) {
		class := "kuttl_execution_failure"
		message := strings.TrimSpace(err.Error())
		if message == "" {
			message = "KUTTL execution failed"
		}
		fmt.Fprintln(stderr, message)
		return kuttlResult{
			ScenarioResult: "not_run",
			RunnerResult:   "error",
			FailureClass:   &class,
			FailureMessage: &message,
			Output:         output,
			Err:            err,
		}
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "KUTTL scenario failed"
	}
	fmt.Fprintln(stderr, message)
	return kuttlResult{
		ScenarioResult: "failed",
		RunnerResult:   "passed",
		Output:         output,
		Err:            err,
	}
}

type limitedCapture struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

func newLimitedCapture(limit int64) *limitedCapture {
	return &limitedCapture{limit: limit}
}

func (w *limitedCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.limit - int64(w.buf.Len())
	if remaining <= 0 {
		w.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		w.buf.Write(p[:remaining])
		w.truncated = true
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

func (w *limitedCapture) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.truncated {
		return w.buf.String()
	}
	return w.buf.String() + fmt.Sprintf("\n[spex: command output truncated after %d bytes]\n", w.limit)
}
